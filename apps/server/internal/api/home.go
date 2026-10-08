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
	"plane-lite/server/internal/sanitize"
	"plane-lite/server/internal/softdelete"
)

// quickLink is WorkspaceUserLinkSerializer ("__all__").
type quickLink struct {
	ID        uuid.UUID      `json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at"`
	CreatedBy *uuid.UUID     `json:"created_by"`
	UpdatedBy *uuid.UUID     `json:"updated_by"`
	Title     *string        `json:"title"`
	URL       string         `json:"url"`
	Metadata  jsontext.Value `json:"metadata"`
	Workspace uuid.UUID      `json:"workspace"`
	Project   *uuid.UUID     `json:"project"`
	Owner     uuid.UUID      `json:"owner"`
}

const quickLinkCols = `l.id, l.created_at, l.updated_at, l.deleted_at, l.created_by_id, l.updated_by_id, l.title, l.url,
	l.metadata::text, l.workspace_id, l.project_id, l.owner_id`

func scanQuickLink(row pgx.Row) (*quickLink, error) {
	var (
		l        quickLink
		metadata string
	)
	err := row.Scan(&l.ID, &l.CreatedAt, &l.UpdatedAt, &l.DeletedAt, &l.CreatedBy, &l.UpdatedBy, &l.Title, &l.URL,
		&metadata, &l.Workspace, &l.Project, &l.Owner)
	l.Metadata = jsontext.Value(metadata)
	return &l, err
}

func (a *API) loadQuickLink(ctx context.Context, id uuid.UUID) (*quickLink, error) {
	return scanQuickLink(a.db.QueryRow(ctx, `SELECT `+quickLinkCols+` FROM workspace_user_links l WHERE l.id = $1`, id))
}

// listQuickLinks ports QuickLinkViewSet.list.
func (a *API) listQuickLinks(c *httpx.Ctx) error {
	rows, err := a.db.Query(c.Context(), `SELECT `+quickLinkCols+` FROM workspace_user_links l
		JOIN workspaces w ON w.id = l.workspace_id
		WHERE w.slug = $1 AND l.owner_id = $2 AND l.deleted_at IS NULL ORDER BY l.created_at DESC`, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	links, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*quickLink, error) { return scanQuickLink(row) })
	if err != nil {
		return err
	}
	if links == nil {
		links = []*quickLink{}
	}
	return c.JSON(http.StatusOK, links)
}

var errQuickLinkExists = httpx.Err(http.StatusBadRequest, "URL already exists for this workspace and owner")

// validateQuickLink runs WorkspaceUserLinkSerializer over request.data:
// to_internal_value prefixes a scheme-less url with "http://" (and crashes on
// a truthy non-string url), validate_url checks it with URLValidator.
func (a *API) validateQuickLink(ctx context.Context, c *httpx.Ctx, data *drf.Data, partial bool) (*setList, error) {
	if !data.IsDict() {
		return nil, errViewCrash // data.get on a list
	}
	if u, ok := data.Get("url"); ok && drf.PyTruthy(u) {
		if !u.IsString() {
			return nil, errViewCrash // .startswith on an int, list ...
		}
		if s := u.Str(); !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
			data.Set("url", drf.JSONValue(jsonString("http://"+s)))
		}
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	if !partial {
		v.Require("url")
	}
	urlInvalid := false
	if s, ok := v.Char("url", drf.CharField{}); ok {
		if drf.ValidURL(*s) {
			set.add("url", *s)
		} else {
			urlInvalid = true
		}
	}
	if s, ok := v.Char("title", drf.CharField{MaxLength: 255, AllowNull: true, AllowBlank: true}); ok {
		set.add("title", s)
	}
	if raw, ok := v.JSON("metadata", false); ok {
		set.addCast("metadata", string(raw), "::jsonb")
	}
	project, ok, err := v.PK("project", true, a.liveRowExists(ctx, "projects"))
	if err != nil {
		return nil, err
	}
	if ok {
		set.add("project_id", project)
	}
	if err := a.auditFields(ctx, v, &set); err != nil {
		return nil, err
	}
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	if err := v.Err(); err != nil || urlInvalid {
		body := map[string]any{}
		var he *httpx.Error
		if errors.As(err, &he) {
			for k, msgs := range he.Body.(map[string][]string) {
				body[k] = msgs
			}
		}
		if urlInvalid {
			body["url"] = map[string]string{"error": "Invalid URL format."} // ValidationError({"error": ...})
		}
		return nil, httpx.Body(http.StatusBadRequest, body)
	}
	return &set, nil
}

// createQuickLink ports QuickLinkViewSet.create.
func (a *API) createQuickLink(c *httpx.Ctx) error {
	ctx := c.Context()
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, c.Param("slug")).Scan(&workspaceID); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	set, err := a.validateQuickLink(ctx, c, data, false)
	if err != nil {
		return err
	}
	url := set.args[indexOf(set.cols, "url")].(string)
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspace_user_links
		WHERE url = $1 AND workspace_id = $2 AND owner_id = $3 AND deleted_at IS NULL)`, url, workspaceID, c.User.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return errQuickLinkExists
	}
	set.drop("created_by_id") // save() sets created_by to the requester
	set.add("workspace_id", workspaceID)
	set.add("owner_id", c.User.ID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, set.insertSQL("workspace_user_links"), set.args...).Scan(&id); err != nil {
		return err
	}
	if err := a.syncLinkWorkspace(ctx, id); err != nil {
		return err
	}
	l, err := a.loadQuickLink(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, l)
}

// syncLinkWorkspace is WorkspaceBaseModel.save: a link with a project takes
// that project's workspace.
func (a *API) syncLinkWorkspace(ctx context.Context, id uuid.UUID) error {
	_, err := a.db.Exec(ctx, `UPDATE workspace_user_links l SET workspace_id = p.workspace_id FROM projects p
		WHERE l.id = $1 AND p.id = l.project_id AND l.workspace_id <> p.workspace_id`, id)
	return err
}

// updateQuickLink ports QuickLinkViewSet.partial_update.
func (a *API) updateQuickLink(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var id, workspaceID uuid.UUID
	err = a.db.QueryRow(ctx, `SELECT l.id, l.workspace_id FROM workspace_user_links l JOIN workspaces w ON w.id = l.workspace_id
		WHERE l.id = $1 AND w.slug = $2 AND l.owner_id = $3 AND l.deleted_at IS NULL`,
		pk, c.Param("slug"), c.User.ID).Scan(&id, &workspaceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Detail(http.StatusNotFound, "Quick link not found.")
	}
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	set, err := a.validateQuickLink(ctx, c, data, true)
	if err != nil {
		return err
	}
	if i := indexOf(set.cols, "url"); i >= 0 {
		var exists bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workspace_user_links
			WHERE url = $1 AND workspace_id = $2 AND owner_id = $3 AND deleted_at IS NULL AND id <> $4)`,
			set.args[i], workspaceID, c.User.ID, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errQuickLinkExists
		}
	}
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE workspace_user_links SET `+set.sql()+` WHERE id = $1`, append([]any{id}, set.args...)...); err != nil {
		return err
	}
	if err := a.syncLinkWorkspace(ctx, id); err != nil {
		return err
	}
	l, err := a.loadQuickLink(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, l)
}

// deleteQuickLink ports QuickLinkViewSet.destroy.
func (a *API) deleteQuickLink(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT l.id FROM workspace_user_links l JOIN workspaces w ON w.id = l.workspace_id
		WHERE l.id = $1 AND w.slug = $2 AND l.owner_id = $3 AND l.deleted_at IS NULL`,
		pk, c.Param("slug"), c.User.ID).Scan(&id); err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "workspace_user_links", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// homeWidgetKeys is WorkspaceHomePreference.HomeWidgetKeys without the two
// the view skips (quick_tutorial, new_at_plane), in order.
var homeWidgetKeys = []string{"quick_links", "recents", "my_stickies"}

// getHomePreferences ports WorkspaceHomePreferenceViewSet.get. Each missing
// widget is created with sort_order 1000 minus its position among the
// missing ones (the view's bulk_create ignore_conflicts re-sends the earlier
// keys, which changes nothing). Rows come back newest first.
func (a *API) getHomePreferences(c *httpx.Ctx) error {
	ctx := c.Context()
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, c.Param("slug")).Scan(&workspaceID); err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT key FROM workspace_home_preferences WHERE user_id = $1 AND workspace_id = $2 AND deleted_at IS NULL`,
		c.User.ID, workspaceID)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	counter := 1
	for _, key := range homeWidgetKeys {
		if indexOf(existing, key) >= 0 {
			continue
		}
		if _, err := a.db.Exec(ctx, `INSERT INTO workspace_home_preferences (key, user_id, workspace_id, sort_order, created_at, updated_at)
			VALUES ($1, $2, $3, $4, clock_timestamp(), clock_timestamp()) ON CONFLICT DO NOTHING`,
			key, c.User.ID, workspaceID, float64(1000-counter)); err != nil {
			return err
		}
		counter++
	}
	rows, err = a.db.Query(ctx, `SELECT key, is_enabled, config::text, sort_order FROM workspace_home_preferences
		WHERE user_id = $1 AND workspace_id = $2 AND deleted_at IS NULL ORDER BY created_at DESC`, c.User.ID, workspaceID)
	if err != nil {
		return err
	}
	type pref struct {
		Key       string         `json:"key"`
		IsEnabled bool           `json:"is_enabled"`
		Config    jsontext.Value `json:"config"`
		SortOrder float64        `json:"sort_order"`
	}
	prefs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pref, error) {
		var (
			p      pref
			config string
		)
		err := row.Scan(&p.Key, &p.IsEnabled, &config, &p.SortOrder)
		p.Config = jsontext.Value(config)
		return p, err
	})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, prefs)
}

// patchHomePreference ports WorkspaceHomePreferenceViewSet.patch.
func (a *API) patchHomePreference(c *httpx.Ctx) error {
	ctx := c.Context()
	var id uuid.UUID
	err := a.db.QueryRow(ctx, `SELECT h.id FROM workspace_home_preferences h JOIN workspaces w ON w.id = h.workspace_id
		WHERE h.key = $1 AND w.slug = $2 AND h.user_id = $3 AND h.deleted_at IS NULL ORDER BY h.created_at DESC LIMIT 1`,
		c.Param("key"), c.Param("slug"), c.User.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Detail(http.StatusBadRequest, "Preference not found")
	}
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	if s, ok := v.Char("key", drf.CharField{MaxLength: 255}); ok {
		set.add("key", *s)
	}
	if b, ok := v.Bool("is_enabled"); ok {
		set.add("is_enabled", b)
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
	}
	if err := v.Err(); err != nil {
		return err
	}
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE workspace_home_preferences SET `+set.sql()+` WHERE id = $1`, append([]any{id}, set.args...)...); err != nil {
		return err
	}
	var out struct {
		Key       string  `json:"key"`
		IsEnabled bool    `json:"is_enabled"`
		SortOrder float64 `json:"sort_order"`
	}
	if err := a.db.QueryRow(ctx, `SELECT key, is_enabled, sort_order FROM workspace_home_preferences WHERE id = $1`, id).
		Scan(&out.Key, &out.IsEnabled, &out.SortOrder); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// sticky is StickySerializer ("__all__"). description_binary is read-only
// and nothing writes it, so it is always null.
type sticky struct {
	ID                  uuid.UUID      `json:"id"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           *time.Time     `json:"deleted_at"`
	CreatedBy           *uuid.UUID     `json:"created_by"`
	UpdatedBy           *uuid.UUID     `json:"updated_by"`
	Name                *string        `json:"name"`
	Description         jsontext.Value `json:"description"`
	DescriptionHTML     string         `json:"description_html"`
	DescriptionStripped *string        `json:"description_stripped"`
	DescriptionBinary   *string        `json:"description_binary"`
	LogoProps           jsontext.Value `json:"logo_props"`
	Color               *string        `json:"color"`
	BackgroundColor     *string        `json:"background_color"`
	Workspace           uuid.UUID      `json:"workspace"`
	Owner               uuid.UUID      `json:"owner"`
	SortOrder           float64        `json:"sort_order"`
}

const stickyCols = `s.id, s.created_at, s.updated_at, s.deleted_at, s.created_by_id, s.updated_by_id, s.name, s.description::text,
	s.description_html, s.description_stripped, s.logo_props::text, s.color, s.background_color, s.workspace_id, s.owner_id, s.sort_order`

func scanSticky(row pgx.Row) (*sticky, error) {
	var (
		s                 sticky
		description, logo string
	)
	err := row.Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt, &s.DeletedAt, &s.CreatedBy, &s.UpdatedBy, &s.Name, &description,
		&s.DescriptionHTML, &s.DescriptionStripped, &logo, &s.Color, &s.BackgroundColor, &s.Workspace, &s.Owner, &s.SortOrder)
	s.Description, s.LogoProps = jsontext.Value(description), jsontext.Value(logo)
	return &s, err
}

func (a *API) loadSticky(ctx context.Context, id uuid.UUID) (*sticky, error) {
	return scanSticky(a.db.QueryRow(ctx, `SELECT `+stickyCols+` FROM stickies s WHERE s.id = $1`, id))
}

// escapeLike is Django's connection.ops.prep_for_like_query.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// listStickies ports WorkspaceStickyViewSet.list: the caller's own stickies,
// highest sort_order first, 20 to a page.
func (a *API) listStickies(c *httpx.Ctx) error {
	ctx := c.Context()
	page, err := parseOffsetPageDefault(c, 20)
	if err != nil {
		return err
	}
	where := `FROM stickies s JOIN workspaces w ON w.id = s.workspace_id
		WHERE w.slug = $1 AND s.owner_id = $2 AND s.deleted_at IS NULL`
	args := []any{c.Param("slug"), c.User.ID}
	if q := c.Query("query"); q != "" {
		where += ` AND UPPER(s.description_stripped::text) LIKE UPPER($3)`
		args = append(args, "%"+escapeLike(q)+"%")
	}
	var total int64
	if err := a.db.QueryRow(ctx, `SELECT count(*) `+where, args...).Scan(&total); err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT `+stickyCols+` `+where+` ORDER BY s.sort_order DESC LIMIT $`+itoa(len(args)+1)+` OFFSET $`+itoa(len(args)+2),
		append(args, page.limit+1, page.offset)...)
	if err != nil {
		return err
	}
	stickies, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*sticky, error) { return scanSticky(row) })
	if err != nil {
		return err
	}
	n := len(stickies)
	results := stickies[:min(int64(n), page.limit)]
	if results == nil {
		results = []*sticky{}
	}
	return c.JSON(http.StatusOK, page.response(results, n, total))
}

// validateSticky runs StickySerializer over request.data; a non-empty
// description_html comes back sanitized.
func (a *API) validateSticky(ctx context.Context, c *httpx.Ctx, data *drf.Data) (*setList, error) {
	v := drf.NewValidator(data, c.Loc())
	var set setList
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	if err := a.auditFields(ctx, v, &set); err != nil {
		return nil, err
	}
	if s, ok := v.Char("name", drf.CharField{AllowNull: true, AllowBlank: true}); ok {
		set.add("name", s)
	}
	if raw, ok := v.JSON("description", false); ok {
		set.addCast("description", string(raw), "::jsonb")
	}
	if s, ok := v.Char("description_html", drf.CharField{AllowBlank: true}); ok {
		set.add("description_html", *s)
	}
	v.Char("description_stripped", drf.CharField{AllowNull: true, AllowBlank: true}) // overwritten by save()
	if raw, ok := v.JSON("logo_props", false); ok {
		set.addCast("logo_props", string(raw), "::jsonb")
	}
	if s, ok := v.Char("color", drf.CharField{MaxLength: 255, AllowNull: true, AllowBlank: true}); ok {
		set.add("color", s)
	}
	if s, ok := v.Char("background_color", drf.CharField{MaxLength: 255, AllowNull: true, AllowBlank: true}); ok {
		set.add("background_color", s)
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
	}
	if v.Valid() {
		// validate(): description_html goes through nh3.
		if i := indexOf(set.cols, "description_html"); i >= 0 && set.args[i].(string) != "" {
			clean, _, err := sanitize.HTML(set.args[i].(string))
			if err != nil {
				v.Add("error", "html content is not valid")
			} else {
				set.args[i] = clean
			}
		}
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return &set, nil
}

// createSticky ports WorkspaceStickyViewSet.create. Sticky.save() derives
// description_stripped and, for a new sticky, puts it 10000 after the
// workspace's highest sort_order.
func (a *API) createSticky(c *httpx.Ctx) error {
	ctx := c.Context()
	var workspaceID uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`, c.Param("slug")).Scan(&workspaceID); err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	set, err := a.validateSticky(ctx, c, data)
	if err != nil {
		return err
	}
	html := "<p></p>"
	if i := indexOf(set.cols, "description_html"); i >= 0 {
		html = set.args[i].(string)
	}
	set.drop("created_by_id") // save() sets created_by to the requester
	set.add("description_stripped", stripTags(html))
	var maxOrder *float64
	if err := a.db.QueryRow(ctx, `SELECT max(sort_order) FROM stickies WHERE workspace_id = $1 AND deleted_at IS NULL`, workspaceID).Scan(&maxOrder); err != nil {
		return err
	}
	if maxOrder != nil {
		set.drop("sort_order")
		set.add("sort_order", *maxOrder+10000)
	}
	set.add("workspace_id", workspaceID)
	set.add("owner_id", c.User.ID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, set.insertSQL("stickies"), set.args...).Scan(&id); err != nil {
		return err
	}
	s, err := a.loadSticky(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, s)
}

// allowStickyCreator is allow_permission([], creator=True, model=Sticky,
// level="WORKSPACE"): an active workspace member who created the sticky.
// The empty role list denies everyone else.
func (a *API) allowStickyCreator(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		pk, err := c.UUIDParam("pk")
		if err != nil {
			return err
		}
		ctx := c.Context()
		role, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
		if err != nil {
			return err
		}
		if role == 0 {
			return errNoRole
		}
		var creator bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM stickies WHERE id = $1 AND created_by_id = $2 AND deleted_at IS NULL)`,
			pk, c.User.ID).Scan(&creator); err != nil {
			return err
		}
		if !creator {
			return errNoRole
		}
		return h(c)
	}
}

// stickyObject is get_object: the caller's own live sticky in the workspace.
func (a *API) stickyObject(c *httpx.Ctx) (uuid.UUID, string, error) {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return uuid.Nil, "", err
	}
	var (
		id   uuid.UUID
		html string
	)
	err = a.db.QueryRow(c.Context(), `SELECT s.id, s.description_html FROM stickies s JOIN workspaces w ON w.id = s.workspace_id
		WHERE s.id = $1 AND w.slug = $2 AND s.owner_id = $3 AND s.deleted_at IS NULL`,
		pk, c.Param("slug"), c.User.ID).Scan(&id, &html)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", httpx.Detail(http.StatusNotFound, "No Sticky matches the given query.")
	}
	return id, html, err
}

// updateSticky ports WorkspaceStickyViewSet.partial_update (DRF's stock
// update). save() recomputes description_stripped from the stored html.
func (a *API) updateSticky(c *httpx.Ctx) error {
	ctx := c.Context()
	id, html, err := a.stickyObject(c)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	set, err := a.validateSticky(ctx, c, data)
	if err != nil {
		return err
	}
	if i := indexOf(set.cols, "description_html"); i >= 0 {
		html = set.args[i].(string)
	}
	set.add("description_stripped", stripTags(html))
	set.add("updated_at", time.Now())
	set.add("updated_by_id", c.User.ID) // save() overrides a given updated_by
	if _, err := a.db.Exec(ctx, `UPDATE stickies SET `+set.sql()+` WHERE id = $1`, append([]any{id}, set.args...)...); err != nil {
		return err
	}
	s, err := a.loadSticky(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, s)
}

// deleteSticky ports WorkspaceStickyViewSet.destroy.
func (a *API) deleteSticky(c *httpx.Ctx) error {
	id, _, err := a.stickyObject(c)
	if err != nil {
		return err
	}
	if err := softdelete.Row(c.Context(), a.db, "stickies", id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
