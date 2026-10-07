package api

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// state is StateSerializer. order is the list views' position within the
// state's group; it is absent elsewhere, unless a PATCH set it on the
// instance.
type state struct {
	ID          uuid.UUID `json:"id"`
	ProjectID   uuid.UUID `json:"project_id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	Name        string    `json:"name"`
	Color       string    `json:"color"`
	Group       string    `json:"group"`
	Default     bool      `json:"default"`
	Description string    `json:"description"`
	Sequence    float64   `json:"sequence"`
	Order       *float64  `json:"order,omitzero"`
}

const stateCols = `s.id, s.project_id, s.workspace_id, s.name, s.color, s."group", s."default", s.description, s.sequence`

// stateVisible is StateViewSet.get_queryset's filter for user $1: the
// state's project is unarchived and has the user as an active member, and
// the state is not a triage state (StateManager plus is_triage=False).
const stateVisible = `s.deleted_at IS NULL AND s."group" <> 'triage' AND NOT s.is_triage AND p.archived_at IS NULL
	AND EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.member_id = $1 AND pm.is_active)`

const stateFrom = ` FROM states s JOIN projects p ON p.id = s.project_id JOIN workspaces w ON w.id = s.workspace_id`

func (a *API) queryStates(ctx context.Context, sql string, args ...any) ([]*state, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*state{}
	for rows.Next() {
		var st state
		if err := rows.Scan(&st.ID, &st.ProjectID, &st.WorkspaceID, &st.Name, &st.Color, &st.Group, &st.Default,
			&st.Description, &st.Sequence); err != nil {
			return nil, err
		}
		out = append(out, &st)
	}
	return out, rows.Err()
}

func (a *API) loadState(ctx context.Context, id uuid.UUID) (*state, error) {
	states, err := a.queryStates(ctx, `SELECT `+stateCols+` FROM states s WHERE s.id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, pgx.ErrNoRows
	}
	return states[0], nil
}

// orderStates sets each state's order: its 1-based position within its
// group divided by the group's size.
func orderStates(states []*state) {
	count := map[string]int{}
	for _, st := range states {
		count[st.Group]++
	}
	seen := map[string]int{}
	for _, st := range states {
		seen[st.Group]++
		o := float64(seen[st.Group]) / float64(count[st.Group])
		st.Order = &o
	}
}

// listStates ports StateViewSet.list (?grouped=true groups by state group).
func (a *API) listStates(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	states, err := a.queryStates(c.Context(), `SELECT `+stateCols+stateFrom+`
		WHERE w.slug = $2 AND s.project_id = $3 AND `+stateVisible+` ORDER BY s.sequence`,
		c.User.ID, c.Param("slug"), projectID)
	if err != nil {
		return err
	}
	orderStates(states)
	if c.Query("grouped") == "true" {
		grouped := map[string][]*state{}
		for _, st := range states {
			grouped[st.Group] = append(grouped[st.Group], st)
		}
		return c.JSON(http.StatusOK, grouped)
	}
	return c.JSON(http.StatusOK, states)
}

// workspaceStates ports WorkspaceStatesEndpoint.get: the states of every
// unarchived project the user is in. The membership is a join, so a user
// with several rows for a project sees its states more than once.
func (a *API) workspaceStates(c *httpx.Ctx) error {
	states, err := a.queryStates(c.Context(), `SELECT `+stateCols+stateFrom+`
		JOIN project_members pm ON pm.project_id = p.id
		WHERE w.slug = $1 AND pm.member_id = $2 AND pm.is_active AND p.archived_at IS NULL
			AND s.deleted_at IS NULL AND s."group" <> 'triage' AND NOT s.is_triage
		ORDER BY s.sequence`, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	orderStates(states)
	return c.JSON(http.StatusOK, states)
}

// getState ports StateViewSet.retrieve (DRF's stock retrieve).
func (a *API) getState(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	states, err := a.queryStates(c.Context(), `SELECT `+stateCols+stateFrom+`
		WHERE w.slug = $2 AND s.project_id = $3 AND s.id = $4 AND `+stateVisible,
		c.User.ID, c.Param("slug"), projectID, pk)
	if err != nil {
		return err
	}
	if len(states) == 0 {
		return httpx.Detail(http.StatusNotFound, "No State matches the given query.")
	}
	return c.JSON(http.StatusOK, states[0])
}

// intakeState ports IntakeStateEndpoint.get: the project's triage state.
func (a *API) intakeState(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	states, err := a.queryStates(c.Context(), `SELECT `+stateCols+` FROM states s JOIN workspaces w ON w.id = s.workspace_id
		WHERE w.slug = $1 AND s.project_id = $2 AND s."group" = 'triage' AND s.deleted_at IS NULL
		ORDER BY s.sequence LIMIT 1`, c.Param("slug"), projectID)
	if err != nil {
		return err
	}
	if len(states) == 0 {
		return httpx.Err(http.StatusNotFound, "Triage state not found")
	}
	return c.JSON(http.StatusOK, states[0])
}

var stateGroups = []string{"backlog", "unstarted", "started", "completed", "cancelled", "triage"}

// errStateNameTaken is the views' answer to a unique violation.
var errStateNameTaken = httpx.Body(http.StatusBadRequest, map[string]any{"name": "The state name is already taken"})

// validateState runs StateSerializer over request.data. order is a
// serializer-only field: valid input comes back separately.
func validateState(v *drf.Validator, partial bool) (*setList, *float64) {
	var set setList
	if !partial {
		v.Require("name", "color")
	}
	if s, ok := v.Char("name", drf.CharField{MaxLength: 255}); ok {
		set.add("name", *s)
	}
	if s, ok := v.Char("color", drf.CharField{MaxLength: 255}); ok {
		set.add("color", *s)
	}
	var group *string
	if s, ok := v.Choice("group", stateGroups, drf.ChoiceField{}); ok {
		group = s
		set.add(`"group"`, *s)
	}
	if b, ok := v.Bool("default"); ok {
		set.add(`"default"`, b)
	}
	if s, ok := v.Char("description", drf.CharField{AllowBlank: true}); ok {
		set.add("description", *s)
	}
	if f, ok := v.Float("sequence"); ok {
		set.add("sequence", f)
	}
	var order *float64
	if f, ok := v.Float("order"); ok {
		order = &f
	}
	if v.Valid() && group != nil && *group == "triage" {
		v.Add("non_field_errors", "Cannot create triage state")
	}
	return &set, order
}

// createState ports StateViewSet.create. State.save() derives the slug and
// puts the state after the project's others (max sequence + 15000).
func (a *API) createState(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	set, order := validateState(v, false)
	if err := v.Err(); err != nil {
		return err
	}
	if order != nil {
		return errViewCrash // State(order=...) is not a model field
	}
	ctx := c.Context()
	var (
		workspaceID uuid.UUID
		maxSeq      *float64
	)
	if err := a.db.QueryRow(ctx, `
		SELECT p.workspace_id, (SELECT max(sequence) FROM states
			WHERE project_id = p.id AND deleted_at IS NULL AND "group" <> 'triage')
		FROM projects p WHERE p.id = $1`, projectID).Scan(&workspaceID, &maxSeq); err != nil {
		return err
	}
	if maxSeq != nil {
		set.drop("sequence")
		set.add("sequence", *maxSeq+15000)
	}
	set.add("slug", drf.Slugify(set.args[slices.Index(set.cols, "name")].(string)))
	set.add("project_id", projectID)
	set.add("workspace_id", workspaceID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, set.insertSQL("states"), set.args...).Scan(&id); err != nil {
		if db.IsUniqueViolation(err) {
			return errStateNameTaken
		}
		if db.IsIntegrityError(err) {
			return errViewCrash // the view returns None
		}
		return err
	}
	st, err := a.loadState(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, st)
}

// stateRow is a state as State.objects.get(pk=..., project_id=...,
// workspace__slug=...) finds it.
type stateRow struct {
	id        uuid.UUID
	name      string
	isDefault bool
}

func (a *API) stateObject(ctx context.Context, slug string, projectID, pk uuid.UUID) (*stateRow, error) {
	var st stateRow
	err := a.db.QueryRow(ctx, `SELECT s.id, s.name, s."default" FROM states s JOIN workspaces w ON w.id = s.workspace_id
		WHERE s.id = $1 AND s.project_id = $2 AND w.slug = $3 AND s.deleted_at IS NULL AND s."group" <> 'triage'`,
		pk, projectID, slug).Scan(&st.id, &st.name, &st.isDefault)
	return &st, err
}

// updateState ports StateViewSet.partial_update.
func (a *API) updateState(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	cur, err := a.stateObject(ctx, c.Param("slug"), projectID, pk)
	if err != nil {
		return err
	}
	id := cur.id
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	set, order := validateState(v, true)
	if err := v.Err(); err != nil {
		return err
	}
	name := cur.name
	if i := slices.Index(set.cols, "name"); i >= 0 {
		name = set.args[i].(string)
	}
	set.add("slug", drf.Slugify(name)) // save() recomputes it every time
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE states SET `+set.sql()+` WHERE id = $1`, append([]any{id}, set.args...)...); err != nil {
		if db.IsUniqueViolation(err) {
			return errStateNameTaken
		}
		if db.IsIntegrityError(err) {
			return errViewCrash
		}
		return err
	}
	st, err := a.loadState(ctx, id)
	if err != nil {
		return err
	}
	st.Order = order
	return c.JSON(http.StatusOK, st)
}

// markDefaultState ports StateViewSet.mark_as_default: two queryset updates
// (no updated_at), so an unknown or triage pk leaves no default at all.
func (a *API) markDefaultState(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	const scope = ` FROM workspaces w WHERE w.id = s.workspace_id AND w.slug = $1 AND s.project_id = $2
		AND s.deleted_at IS NULL AND s."group" <> 'triage'`
	if _, err := a.db.Exec(ctx, `UPDATE states s SET "default" = false`+scope+` AND s."default"`, c.Param("slug"), projectID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE states s SET "default" = true`+scope+` AND s.id = $3`, c.Param("slug"), projectID, pk); err != nil {
		return err
	}
	return c.NoContent()
}

// deleteState ports StateViewSet.destroy.
func (a *API) deleteState(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	st, err := a.stateObject(ctx, c.Param("slug"), projectID, pk)
	if err != nil {
		return err
	}
	id := st.id
	if st.isDefault {
		return httpx.Err(http.StatusBadRequest, "Default state cannot be deleted")
	}
	var used bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issues WHERE state_id = $1 AND deleted_at IS NULL)`, id).Scan(&used); err != nil {
		return err
	}
	if used {
		return httpx.Err(http.StatusBadRequest, "The state is not empty, only empty states can be deleted")
	}
	if err := softdelete.Row(ctx, a.db, "states", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
