package api

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// label is LabelSerializer.
type label struct {
	Parent      *uuid.UUID `json:"parent"`
	Name        string     `json:"name"`
	Color       string     `json:"color"`
	ID          uuid.UUID  `json:"id"`
	ProjectID   *uuid.UUID `json:"project_id"`
	WorkspaceID uuid.UUID  `json:"workspace_id"`
	SortOrder   float64    `json:"sort_order"`
}

const labelCols = `l.parent_id, l.name, l.color, l.id, l.project_id, l.workspace_id, l.sort_order`

// labelVisible is LabelViewSet.get_queryset for user $1, slug $2 and
// project $3: the project's live labels, provided the user has a
// membership row of any kind in it.
const labelVisible = ` FROM labels l JOIN workspaces w ON w.id = l.workspace_id
	WHERE w.slug = $2 AND l.project_id = $3 AND l.deleted_at IS NULL
		AND EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = l.project_id AND pm.member_id = $1)`

func (a *API) queryLabels(ctx context.Context, sql string, args ...any) ([]*label, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*label, error) {
		var l label
		err := row.Scan(&l.Parent, &l.Name, &l.Color, &l.ID, &l.ProjectID, &l.WorkspaceID, &l.SortOrder)
		return &l, err
	})
}

func (a *API) loadLabel(ctx context.Context, id uuid.UUID) (*label, error) {
	labels, err := a.queryLabels(ctx, `SELECT `+labelCols+` FROM labels l WHERE l.id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return nil, pgx.ErrNoRows
	}
	return labels[0], nil
}

// listLabels ports LabelViewSet.list (DRF's stock list).
func (a *API) listLabels(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	labels, err := a.queryLabels(c.Context(), `SELECT `+labelCols+labelVisible+` ORDER BY l.sort_order`,
		c.User.ID, c.Param("slug"), projectID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, labels)
}

// workspaceLabels ports WorkspaceLabelsEndpoint.get: the labels of every
// unarchived project the user is active in. Django caches this per user for
// two hours and misses invalidations; it is computed per request here.
func (a *API) workspaceLabels(c *httpx.Ctx) error {
	labels, err := a.queryLabels(c.Context(), `SELECT `+labelCols+` FROM labels l
		JOIN workspaces w ON w.id = l.workspace_id
		JOIN projects p ON p.id = l.project_id
		JOIN project_members pm ON pm.project_id = p.id
		WHERE w.slug = $1 AND pm.member_id = $2 AND pm.is_active AND p.archived_at IS NULL AND l.deleted_at IS NULL
		ORDER BY l.created_at DESC`, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, labels)
}

var errLabelNameTaken = httpx.Err(http.StatusBadRequest, "Label with the same name already exists in the project")

// validateLabel runs LabelSerializer over request.data. current is the
// label being updated (nil on create).
func (a *API) validateLabel(ctx context.Context, v *drf.Validator, projectID uuid.UUID, current *uuid.UUID, partial bool) (*setList, error) {
	var set setList
	if !partial {
		v.Require("name")
	}
	parent, ok, err := v.PK("parent", true, a.liveRowExists(ctx, "labels"))
	if err != nil {
		return nil, err
	}
	if ok {
		set.add("parent_id", parent)
	}
	if name, ok := v.Char("name", drf.CharField{MaxLength: 255}); ok {
		// validate_name: unique, ignoring case, among the project's labels.
		var taken bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM labels WHERE project_id = $1
			AND upper(name) = upper($2) AND deleted_at IS NULL AND id IS DISTINCT FROM $3)`,
			projectID, *name, current).Scan(&taken); err != nil {
			return nil, err
		}
		if taken {
			v.Add("name", "LABEL_NAME_ALREADY_EXISTS")
		} else {
			set.add("name", *name)
		}
	}
	if s, ok := v.Char("color", drf.CharField{MaxLength: 255, AllowBlank: true}); ok {
		set.add("color", *s)
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
	}
	return &set, nil
}

// createLabel ports LabelViewSet.create. Label.save() puts the label after
// the project's others (max sort_order + 10000).
func (a *API) createLabel(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	ctx := c.Context()
	v := drf.NewValidator(data, c.Loc())
	set, err := a.validateLabel(ctx, v, projectID, nil, false)
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return err
	}
	var (
		workspaceID uuid.UUID
		maxSort     *float64
	)
	if err := a.db.QueryRow(ctx, `
		SELECT p.workspace_id, (SELECT max(sort_order) FROM labels WHERE project_id = p.id AND deleted_at IS NULL)
		FROM projects p WHERE p.id = $1`, projectID).Scan(&workspaceID, &maxSort); err != nil {
		return err
	}
	if maxSort != nil {
		set.drop("sort_order")
		set.add("sort_order", *maxSort+10000)
	}
	set.add("project_id", projectID)
	set.add("workspace_id", workspaceID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, set.insertSQL("labels"), set.args...).Scan(&id); err != nil {
		if db.IsIntegrityError(err) {
			return errLabelNameTaken
		}
		return err
	}
	l, err := a.loadLabel(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, l)
}

// labelObject is get_object(): a label of the queryset, else DRF's 404.
func (a *API) labelObject(c *httpx.Ctx, projectID uuid.UUID) (uuid.UUID, error) {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return uuid.Nil, err
	}
	labels, err := a.queryLabels(c.Context(), `SELECT `+labelCols+labelVisible+` AND l.id = $4`,
		c.User.ID, c.Param("slug"), projectID, pk)
	if err != nil {
		return uuid.Nil, err
	}
	if len(labels) == 0 {
		return uuid.Nil, httpx.Detail(http.StatusNotFound, "No Label matches the given query.")
	}
	return labels[0].ID, nil
}

// updateLabel ports LabelViewSet.partial_update. An exact name match in the
// project is refused before the label is even looked up.
func (a *API) updateLabel(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	ctx := c.Context()
	hasName, err := dataContains(data, "name")
	if err != nil {
		return err
	}
	if hasName {
		if !data.IsDict() {
			return errViewCrash // request.data["name"] on a list or str
		}
		name, _ := data.Get("name")
		if name.Kind() == '[' || name.Kind() == '{' {
			return errViewCrash
		}
		var taken bool
		if !name.IsNull() {
			if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM labels
				WHERE project_id = $1 AND name = $2 AND deleted_at IS NULL AND id <> $3)`,
				projectID, drf.PyStr(name), pk).Scan(&taken); err != nil {
				return err
			}
		}
		if taken {
			return errLabelNameTaken
		}
	}
	id, err := a.labelObject(c, projectID)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	set, err := a.validateLabel(ctx, v, projectID, &id, true)
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return err
	}
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE labels SET `+set.sql()+` WHERE id = $1`, append([]any{id}, set.args...)...); err != nil {
		return err
	}
	l, err := a.loadLabel(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, l)
}

// deleteLabel ports LabelViewSet.destroy: a soft delete that cascades to
// child labels.
func (a *API) deleteLabel(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	id, err := a.labelObject(c, projectID)
	if err != nil {
		return err
	}
	if err := softdelete.Row(c.Context(), a.db, "labels", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
