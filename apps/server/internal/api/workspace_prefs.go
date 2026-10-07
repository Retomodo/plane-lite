package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

func itoa(n int) string { return strconv.Itoa(n) }

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// workspaceUserProperties is WorkspaceUserPropertiesSerializer ("__all__").
type workspaceUserProperties struct {
	ID                          uuid.UUID      `json:"id"`
	CreatedAt                   time.Time      `json:"created_at"`
	UpdatedAt                   time.Time      `json:"updated_at"`
	DeletedAt                   *time.Time     `json:"deleted_at"`
	CreatedBy                   *uuid.UUID     `json:"created_by"`
	UpdatedBy                   *uuid.UUID     `json:"updated_by"`
	Workspace                   uuid.UUID      `json:"workspace"`
	User                        uuid.UUID      `json:"user"`
	Filters                     jsontext.Value `json:"filters"`
	DisplayFilters              jsontext.Value `json:"display_filters"`
	DisplayProperties           jsontext.Value `json:"display_properties"`
	RichFilters                 jsontext.Value `json:"rich_filters"`
	NavigationProjectLimit      int            `json:"navigation_project_limit"`
	NavigationControlPreference string         `json:"navigation_control_preference"`
}

// workspaceUserPropertiesFor is WorkspaceUserProperties.objects.get_or_create
// for the user in the workspace with this slug.
func (a *API) workspaceUserPropertiesFor(ctx context.Context, slug string, user uuid.UUID) (*workspaceUserProperties, error) {
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, slug).Scan(&workspaceID); err != nil {
		return nil, err
	}
	if _, err := a.db.Exec(ctx, `
		INSERT INTO workspace_user_properties (workspace_id, user_id, created_by_id)
		SELECT $1, $2, $2 WHERE NOT EXISTS (
			SELECT 1 FROM workspace_user_properties WHERE workspace_id = $1 AND user_id = $2 AND deleted_at IS NULL)
		ON CONFLICT DO NOTHING`, workspaceID, user); err != nil {
		return nil, err
	}
	var (
		p                                    workspaceUserProperties
		filters, display, props, richFilters string
	)
	err := a.db.QueryRow(ctx, `
		SELECT id, created_at, updated_at, deleted_at, created_by_id, updated_by_id, workspace_id, user_id,
			filters::text, display_filters::text, display_properties::text, rich_filters::text,
			navigation_project_limit, navigation_control_preference
		FROM workspace_user_properties WHERE workspace_id = $1 AND user_id = $2 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, workspaceID, user,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.CreatedBy, &p.UpdatedBy, &p.Workspace, &p.User,
		&filters, &display, &props, &richFilters, &p.NavigationProjectLimit, &p.NavigationControlPreference)
	if err != nil {
		return nil, err
	}
	p.Filters, p.DisplayFilters = jsontext.Value(filters), jsontext.Value(display)
	p.DisplayProperties, p.RichFilters = jsontext.Value(props), jsontext.Value(richFilters)
	return &p, nil
}

// getWorkspaceUserProperties ports WorkspaceUserPropertiesEndpoint.get.
func (a *API) getWorkspaceUserProperties(c *httpx.Ctx) error {
	p, err := a.workspaceUserPropertiesFor(c.Context(), c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

var navigationControlPreferences = []string{"ACCORDION", "TABBED"}

// patchWorkspaceUserProperties ports WorkspaceUserPropertiesEndpoint.patch.
func (a *API) patchWorkspaceUserProperties(c *httpx.Ctx) error {
	ctx := c.Context()
	p, err := a.workspaceUserPropertiesFor(ctx, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	for _, col := range []string{"filters", "display_filters", "display_properties", "rich_filters"} {
		if raw, ok := v.JSON(col, false); ok {
			set.addCast(col, string(raw), "::jsonb")
		}
	}
	if n, ok := v.Int("navigation_project_limit", -2147483648, 2147483647); ok {
		set.add("navigation_project_limit", n)
	}
	if s, ok := v.Choice("navigation_control_preference", navigationControlPreferences, drf.ChoiceField{}); ok {
		set.add("navigation_control_preference", *s)
	}
	if err := a.auditFields(ctx, v, &set); err != nil {
		return err
	}
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	if err := v.Err(); err != nil {
		return err
	}
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE workspace_user_properties SET `+set.sql()+` WHERE id = $1`,
		append([]any{p.ID}, set.args...)...); err != nil {
		return err
	}
	updated, err := a.workspaceUserPropertiesFor(ctx, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, updated)
}

// sidebarKeys is WorkspaceUserPreference.UserPreferenceKeys, in order.
var sidebarKeys = []string{"views", "active_cycles", "analytics", "drafts", "your_work", "archives", "stickies"}

// getSidebarPreferences ports WorkspaceUserPreferenceViewSet.get: missing
// keys are created (pinned for drafts, your_work and stickies), numbered
// by their position among the missing ones.
func (a *API) getSidebarPreferences(c *httpx.Ctx) error {
	ctx := c.Context()
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, c.Param("slug")).Scan(&workspaceID); err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `
		SELECT key FROM workspace_user_preferences WHERE user_id = $1 AND workspace_id = $2 AND deleted_at IS NULL`,
		c.User.ID, workspaceID)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, k := range existing {
		have[k] = true
	}
	i := 0
	for _, key := range sidebarKeys {
		if have[key] {
			continue
		}
		pinned := key == "drafts" || key == "your_work" || key == "stickies"
		if _, err := a.db.Exec(ctx, `
			INSERT INTO workspace_user_preferences (key, user_id, workspace_id, sort_order, is_pinned)
			VALUES ($1, $2, $3, $4, $5) ON CONFLICT DO NOTHING`,
			key, c.User.ID, workspaceID, 65535+i*10000, pinned); err != nil {
			return err
		}
		i++
	}
	rows, err = a.db.Query(ctx, `
		SELECT key, is_pinned, sort_order FROM workspace_user_preferences
		WHERE user_id = $1 AND workspace_id = $2 AND deleted_at IS NULL ORDER BY sort_order`, c.User.ID, workspaceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var (
			key    string
			pinned bool
			order  float64
		)
		if err := rows.Scan(&key, &pinned, &order); err != nil {
			return err
		}
		out[key] = map[string]any{"is_pinned": pinned, "sort_order": order}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// patchSidebarPreferences ports WorkspaceUserPreferenceViewSet.patch: a list
// of {key, is_pinned?, sort_order?}, each saved on its own (no transaction)
// with save(update_fields=[...]), so updated_at is left alone.
func (a *API) patchSidebarPreferences(c *httpx.Ctx) error {
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	ctx := c.Context()
	root := data.Root()
	var entries []drf.Value
	switch root.Kind() {
	case '[':
		entries, _ = root.Elems()
	case '{':
		if string(root.Raw()) != "{}" {
			return errViewCrash // iterating a dict yields str keys
		}
	default:
		if !data.IsDict() {
			return errViewCrash
		}
	}
	for _, e := range entries {
		if e.Kind() != '{' {
			return errViewCrash
		}
		key, _ := e.Member("key")
		if !drf.PyTruthy(key) {
			continue
		}
		var (
			id       uuid.UUID
			isPinned bool
			order    float64
		)
		err := a.db.QueryRow(ctx, `
			SELECT p.id, p.is_pinned, p.sort_order FROM workspace_user_preferences p
			JOIN workspaces w ON w.id = p.workspace_id
			WHERE p.key = $1 AND w.slug = $2 AND p.user_id = $3 AND p.deleted_at IS NULL
			ORDER BY p.created_at DESC LIMIT 1`, drf.PyStr(key), c.Param("slug"), c.User.ID).Scan(&id, &isPinned, &order)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var pinnedArg, orderArg any = isPinned, order
		if v, ok := e.Member("is_pinned"); ok {
			b, err := djangoBool(v)
			if err != nil {
				return err
			}
			pinnedArg = b
		}
		if v, ok := e.Member("sort_order"); ok {
			f, ok := pyFloat(v)
			if !ok {
				return errViewCrash
			}
			orderArg = f
		}
		if _, err := a.db.Exec(ctx, `UPDATE workspace_user_preferences SET is_pinned = $2, sort_order = $3 WHERE id = $1`,
			id, pinnedArg, orderArg); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"message": "Successfully updated"})
}

// pyFloat is FloatField.get_prep_value: float(value), None kept as NULL.
func pyFloat(v drf.Value) (*float64, bool) {
	var f float64
	switch v.Kind() {
	case 'n':
		return nil, true
	case 't':
		f = 1
	case 'f':
		f = 0
	case '0':
		var err error
		if f, err = strconv.ParseFloat(string(v.Raw()), 64); err != nil {
			return nil, false
		}
	case '"':
		var err error
		if f, err = strconv.ParseFloat(drf.PyStrip(v.Str()), 64); err != nil {
			return nil, false
		}
	default:
		return nil, false
	}
	return &f, true
}

// saveWorkspaceViewProps ports WorkspaceMemberUserViewsEndpoint.post.
func (a *API) saveWorkspaceViewProps(c *httpx.Ctx) error {
	ctx := c.Context()
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `
		SELECT wm.id FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
		WHERE w.slug = $1 AND wm.member_id = $2 AND wm.is_active AND wm.deleted_at IS NULL`,
		c.Param("slug"), c.User.ID).Scan(&id); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	var props any = "{}"
	if v, ok := data.Get("view_props"); ok {
		props = string(v.Raw())
		if v.IsNull() {
			props = nil // JSONField stores None as SQL NULL: a not-null violation
		}
	}
	if _, err := a.db.Exec(ctx, `
		UPDATE workspace_members SET view_props = $2::jsonb, updated_at = now(), updated_by_id = $3 WHERE id = $1`,
		id, props, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
