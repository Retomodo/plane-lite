package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// ModuleLinkViewSet (P/modules/<module_id>/module-links/).

// moduleLink is ModuleLinkSerializer: every model field.
type moduleLink struct {
	ID        uuid.UUID      `json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at"`
	Title     *string        `json:"title"`
	URL       string         `json:"url"`
	Metadata  jsontext.Value `json:"metadata"`
	CreatedBy *uuid.UUID     `json:"created_by"`
	UpdatedBy *uuid.UUID     `json:"updated_by"`
	Project   uuid.UUID      `json:"project"`
	Workspace uuid.UUID      `json:"workspace"`
	Module    uuid.UUID      `json:"module"`
}

const moduleLinkSelect = `SELECT l.id, l.created_at, l.updated_at, l.deleted_at, l.title, l.url, l.metadata::text,
		l.created_by_id, l.updated_by_id, l.project_id, l.workspace_id, l.module_id
	FROM module_links l`

func scanModuleLink(row pgx.Row) (*moduleLink, error) {
	var (
		l    moduleLink
		meta string
	)
	if err := row.Scan(&l.ID, &l.CreatedAt, &l.UpdatedAt, &l.DeletedAt, &l.Title, &l.URL, &meta, &l.CreatedBy,
		&l.UpdatedBy, &l.Project, &l.Workspace, &l.Module); err != nil {
		return nil, err
	}
	l.Metadata = jsontext.Value(meta)
	return &l, nil
}

// moduleLinksOf is the link_module prefetch: the module's live links,
// newest first.
func (a *API) moduleLinksOf(ctx context.Context, moduleID uuid.UUID) ([]*moduleLink, error) {
	rows, err := a.db.Query(ctx, moduleLinkSelect+` WHERE l.deleted_at IS NULL AND l.module_id = $1
		ORDER BY l.created_at DESC`, moduleID)
	if err != nil {
		return nil, err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*moduleLink, error) { return scanModuleLink(row) })
	if links == nil {
		links = []*moduleLink{}
	}
	return links, err
}

// moduleLinkObject is get_object(): the link through get_queryset (the
// module's live links, for a user with an active membership row of the
// unarchived project), or DRF's 404.
func (a *API) moduleLinkObject(c *httpx.Ctx) (*moduleLink, error) {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return nil, err
	}
	moduleID, err := c.UUIDParam("module_id")
	if err != nil {
		return nil, err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return nil, err
	}
	l, err := scanModuleLink(a.db.QueryRow(c.Context(), moduleLinkSelect+`
		JOIN workspaces w ON w.id = l.workspace_id JOIN projects pr ON pr.id = l.project_id
		WHERE l.deleted_at IS NULL AND w.slug = $1 AND l.project_id = $2 AND l.module_id = $3 AND l.id = $4
			AND pr.archived_at IS NULL AND EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = l.project_id
				AND pm.member_id = $5 AND pm.is_active)`,
		c.Param("slug"), projectID, moduleID, pk, c.User.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errModuleLinkNotFound
	}
	return l, err
}

var (
	errModuleLinkNotFound = httpx.Detail(http.StatusNotFound, "No ModuleLink matches the given query.")
	errModuleLinkBadURL   = httpx.Err(http.StatusBadRequest, "Invalid URL format.")
)

// moduleLinkInput is ModuleLinkSerializer's validated data; nil fields
// were not sent.
type moduleLinkInput struct {
	title     **string
	url       *string
	metadata  jsontext.Value
	deletedAt **time.Time
}

// validateModuleLink runs ModuleLinkSerializer over request.data, which its
// to_internal_value first edits: a truthy url without an http(s) scheme
// gets "http://" prepended (a non-string one crashes). url is the model's
// URLField (max 200, Django's URLValidator).
func validateModuleLink(data *drf.Data, loc *time.Location, partial bool) (*moduleLinkInput, error) {
	if !data.IsDict() {
		return nil, errViewCrash // data.get on a list
	}
	if raw, ok := data.Get("url"); ok && drf.PyTruthy(raw) {
		if !raw.IsString() {
			return nil, errViewCrash
		}
		if s := raw.Str(); !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			data.Set("url", drf.JSONValue(jsonString("http://"+s)))
		}
	}
	v := drf.NewValidator(data, loc)
	in := &moduleLinkInput{}
	if !partial {
		v.Require("url")
	}
	if data.Has("deleted_at") {
		if t, ok := v.DateTime("deleted_at", true); ok {
			in.deletedAt = &t
		}
	}
	if data.Has("title") {
		if s, ok := v.Char("title", drf.CharField{MaxLength: 255, AllowBlank: true, AllowNull: true}); ok {
			in.title = &s
		}
	}
	if s, ok := v.Char("url", drf.CharField{MaxLength: 200, URL: true}); ok {
		in.url = s
	}
	if m, ok := v.JSON("metadata", false); ok {
		in.metadata = m
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// moduleLinkURLTaken is the serializers' duplicate check: a live link of
// the module with this url.
func (a *API) moduleLinkURLTaken(ctx context.Context, url string, moduleID uuid.UUID, except *uuid.UUID) (bool, error) {
	var taken bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM module_links WHERE url = $1 AND module_id = $2
		AND deleted_at IS NULL AND id IS DISTINCT FROM $3)`, url, moduleID, except).Scan(&taken)
	return taken, err
}

// createModuleLink ports ModuleLinkViewSet.create (DRF's default, saving
// project_id and module_id from the URL). The module is not checked: a
// missing one fails the foreign key.
func (a *API) createModuleLink(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	moduleID, err := c.UUIDParam("module_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	in, err := validateModuleLink(data, c.Loc(), false)
	if err != nil {
		return err
	}
	// ModuleLinkSerializer.create: validate_url never fails here (the
	// field's URLValidator already ran).
	if taken, err := a.moduleLinkURLTaken(ctx, *in.url, moduleID, nil); err != nil {
		return err
	} else if taken {
		return httpx.Err(http.StatusBadRequest, "URL already exists.")
	}
	var title *string
	if in.title != nil {
		title = *in.title
	}
	var deletedAt *time.Time
	if in.deletedAt != nil {
		deletedAt = *in.deletedAt
	}
	metadata := in.metadata
	if metadata == nil {
		metadata = jsontext.Value("{}")
	}
	id := uuid.New()
	if _, err := a.db.Exec(ctx, `INSERT INTO module_links (id, created_at, updated_at, deleted_at, title, url, metadata,
			created_by_id, project_id, workspace_id, module_id)
		SELECT $1, now(), now(), $2, $3, $4, $5::jsonb, $6, p.id, p.workspace_id, $8 FROM projects p WHERE p.id = $7`,
		id, deletedAt, title, *in.url, string(metadata), c.User.ID, projectID, moduleID); err != nil {
		return err
	}
	link, err := scanModuleLink(a.db.QueryRow(ctx, moduleLinkSelect+` WHERE l.id = $1`, id))
	if err != nil {
		return err
	}
	// CreateModelMixin.get_success_headers: the serializer's "url" field
	// (api_settings.URL_FIELD_NAME) becomes the Location header.
	c.W.Header().Set("Location", link.URL)
	return c.JSON(http.StatusCreated, link)
}

// updateModuleLink ports ModuleLinkViewSet.partial_update. update() runs
// validate_url on validated_data.get("url"), so a body without a url fails.
func (a *API) updateModuleLink(c *httpx.Ctx) error {
	ctx := c.Context()
	cur, err := a.moduleLinkObject(c)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	in, err := validateModuleLink(data, c.Loc(), true)
	if err != nil {
		return err
	}
	if in.url == nil {
		return errModuleLinkBadURL
	}
	if taken, err := a.moduleLinkURLTaken(ctx, *in.url, cur.Module, &cur.ID); err != nil {
		return err
	} else if taken {
		return httpx.Err(http.StatusBadRequest, "URL already exists for this Issue")
	}
	title, metadata, deletedAt := cur.Title, cur.Metadata, cur.DeletedAt
	if in.title != nil {
		title = *in.title
	}
	if in.metadata != nil {
		metadata = in.metadata
	}
	if in.deletedAt != nil {
		deletedAt = *in.deletedAt
	}
	if _, err := a.db.Exec(ctx, `UPDATE module_links SET title = $2, url = $3, metadata = $4::jsonb, deleted_at = $5,
			updated_at = now(), updated_by_id = $6 WHERE id = $1`,
		cur.ID, title, *in.url, string(metadata), deletedAt, c.User.ID); err != nil {
		return err
	}
	link, err := scanModuleLink(a.db.QueryRow(ctx, moduleLinkSelect+` WHERE l.id = $1`, cur.ID))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, link)
}

// deleteModuleLink ports ModuleLinkViewSet.destroy.
func (a *API) deleteModuleLink(c *httpx.Ctx) error {
	cur, err := a.moduleLinkObject(c)
	if err != nil {
		return err
	}
	if err := softdelete.Row(c.Context(), a.db, "module_links", cur.ID, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
