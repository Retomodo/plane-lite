package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/db"
	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// workspace is WorkSpaceSerializer: every Workspace column plus logo_url,
// and the total_members / role annotations when the queryset has them.
type workspace struct {
	ID               uuid.UUID  `json:"id"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	DeletedAt        *time.Time `json:"deleted_at"`
	CreatedBy        *uuid.UUID `json:"created_by"`
	UpdatedBy        *uuid.UUID `json:"updated_by"`
	Name             string     `json:"name"`
	Logo             *string    `json:"logo"`
	LogoAsset        *uuid.UUID `json:"logo_asset"`
	Owner            uuid.UUID  `json:"owner"`
	Slug             string     `json:"slug"`
	OrganizationSize *string    `json:"organization_size"`
	Timezone         string     `json:"timezone"`
	BackgroundColor  string     `json:"background_color"`
	LogoURL          *string    `json:"logo_url"`
	TotalMembers     *int       `json:"total_members,omitzero"`
	Role             *int       `json:"role,omitzero"`
}

const (
	workspaceCols = `w.id, w.created_at, w.updated_at, w.deleted_at, w.created_by_id, w.updated_by_id,
		w.name, w.logo, w.logo_asset_id, w.owner_id, w.slug, w.organization_size, w.timezone,
		w.background_color, fa.entity_type`
	workspaceFrom = `FROM workspaces w LEFT JOIN file_assets fa ON fa.id = w.logo_asset_id`
	// totalMembersSQL is the member_count subquery: active, non-bot members.
	totalMembersSQL = `(SELECT count(*) FROM workspace_members m JOIN users u ON u.id = m.member_id
		WHERE m.workspace_id = w.id AND NOT u.is_bot AND m.is_active AND m.deleted_at IS NULL)`
)

// scanWorkspace reads workspaceCols followed by any extra columns.
func scanWorkspace(row pgx.Row, extra ...any) (*workspace, error) {
	var (
		w        workspace
		logoType *string
	)
	dest := append([]any{&w.ID, &w.CreatedAt, &w.UpdatedAt, &w.DeletedAt, &w.CreatedBy, &w.UpdatedBy,
		&w.Name, &w.Logo, &w.LogoAsset, &w.Owner, &w.Slug, &w.OrganizationSize, &w.Timezone,
		&w.BackgroundColor, &logoType}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	w.LogoURL = imageURL(w.LogoAsset, logoType, w.Logo)
	return &w, nil
}

// workspaceFilter is filter_queryset for views with search_fields=["name"]
// and filterset_fields=["owner"]: DjangoFilterBackend validates ?owner=
// first, then SearchFilter ANDs one icontains per term.
type workspaceFilter struct {
	where []string
	args  []any
}

func (a *API) parseWorkspaceFilter(c *httpx.Ctx, args ...any) (*workspaceFilter, error) {
	f := &workspaceFilter{args: args}
	q := djangoQuery{c}
	if owner := q.Get("owner"); owner != "" {
		id, ok := drf.ParseUUID(owner)
		if !ok {
			return nil, httpx.Body(http.StatusBadRequest, map[string]any{"owner": []string{"“" + owner + "” is not a valid UUID."}})
		}
		var exists bool
		if err := a.db.QueryRow(c.Context(), `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, id).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, httpx.Body(http.StatusBadRequest, map[string]any{
				"owner": []string{"Select a valid choice. That choice is not one of the available choices."}})
		}
		f.args = append(f.args, id)
		f.where = append(f.where, fmt.Sprintf("w.owner_id = $%d", len(f.args)))
	}
	terms, err := drf.SearchTerms(q.Get("search"))
	if err != nil {
		return nil, err
	}
	for _, term := range terms {
		f.args = append(f.args, "%"+likeEscape(term)+"%")
		f.where = append(f.where, fmt.Sprintf("w.name ILIKE $%d", len(f.args)))
	}
	return f, nil
}

func (f *workspaceFilter) sql() string {
	if len(f.where) == 0 {
		return ""
	}
	return " AND " + strings.Join(f.where, " AND ")
}

// likeEscape escapes LIKE wildcards, as Django's icontains lookup does.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// myWorkspaces ports app.views.workspace.base.UserWorkSpacesEndpoint. The
// ?fields= parameter is ignored: DynamicBaseSerializer drops it.
func (a *API) myWorkspaces(c *httpx.Ctx) error {
	ctx := c.Context()
	f, err := a.parseWorkspaceFilter(c, c.User.ID)
	if err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `
		SELECT `+workspaceCols+`, `+totalMembersSQL+`,
			(SELECT m.role FROM workspace_members m
			WHERE m.workspace_id = w.id AND m.member_id = $1 AND m.is_active AND m.deleted_at IS NULL LIMIT 1)
		`+workspaceFrom+`
		WHERE w.deleted_at IS NULL AND EXISTS (
			SELECT 1 FROM workspace_members wm WHERE wm.workspace_id = w.id AND wm.member_id = $1 AND wm.is_active)`+
		f.sql()+`
		ORDER BY w.created_at DESC`, f.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []*workspace{}
	for rows.Next() {
		var total int
		var role *int
		w, err := scanWorkspace(rows, &total, &role)
		if err != nil {
			return err
		}
		w.TotalMembers, w.Role = &total, role
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// workspaceSlugCheck ports WorkSpaceAvailabilityCheckEndpoint.
func (a *API) workspaceSlugCheck(c *httpx.Ctx) error {
	slug := c.Query("slug")
	if slug == "" {
		return httpx.Err(http.StatusBadRequest, "Workspace Slug is required")
	}
	taken := slices.Contains(restrictedWorkspaceSlugs, slug)
	if !taken {
		if err := a.db.QueryRow(c.Context(),
			`SELECT EXISTS (SELECT 1 FROM workspaces WHERE slug = $1 AND deleted_at IS NULL)`, slug).Scan(&taken); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"status": !taken})
}

// listWorkspaces ports WorkSpaceViewSet.list, whose allow_permission
// decorator reads kwargs["slug"] on a URL without one: a KeyError.
func (a *API) listWorkspaces(*httpx.Ctx) error {
	return httpx.Err(http.StatusBadRequest, "The required key does not exist.")
}

// errNoWorkspace is get_object()'s Http404 in WorkSpaceViewSet.
var errNoWorkspace = httpx.Detail(http.StatusNotFound, "No Workspace matches the given query.")

// workspaceObject is WorkSpaceViewSet.get_object(): the workspace with this
// slug among the user's (filtered) workspaces, annotated with total_members.
func (a *API) workspaceObject(c *httpx.Ctx) (*workspace, error) {
	f, err := a.parseWorkspaceFilter(c, c.User.ID, c.Param("slug"))
	if err != nil {
		return nil, err
	}
	var total int
	w, err := scanWorkspace(a.db.QueryRow(c.Context(), `
		SELECT `+workspaceCols+`, `+totalMembersSQL+`
		`+workspaceFrom+`
		WHERE w.deleted_at IS NULL AND w.slug = $2 AND EXISTS (
			SELECT 1 FROM workspace_members wm WHERE wm.workspace_id = w.id AND wm.member_id = $1 AND wm.is_active)`+
		f.sql(), f.args...), &total)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoWorkspace
	}
	if err != nil {
		return nil, err
	}
	w.TotalMembers = &total
	return w, nil
}

// getWorkspace ports WorkSpaceViewSet.retrieve.
func (a *API) getWorkspace(c *httpx.Ctx) error {
	w, err := a.workspaceObject(c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, w)
}

// validateWorkspace runs WorkSpaceSerializer over request.data. current is
// the instance being updated (nil on create).
func (a *API) validateWorkspace(ctx context.Context, v *drf.Validator, current *uuid.UUID, partial bool) (*setList, error) {
	var (
		set   setList
		dbErr error
	)
	if !partial {
		v.Require("name", "slug")
	}
	if name, ok := v.Char("name", drf.CharField{MaxLength: 80}); ok {
		switch {
		case containsURL(*name):
			v.Add("name", "Name must not contain URLs")
		case !hasAlphanumeric(*name):
			v.Add("name", "Name must contain at least one letter or number")
		default:
			set.add("name", *name)
		}
	}
	slug, ok := v.Char("slug", drf.CharField{MaxLength: 48, Slug: true, Before: []func(string) string{
		func(s string) string { // the model's slug_validator
			if slices.Contains(restrictedWorkspaceSlugs, s) {
				return "Slug is not valid"
			}
			return ""
		},
		func(s string) string { // UniqueValidator; a failing lookup counts as unique
			if strings.ContainsRune(s, 0) {
				return ""
			}
			var taken bool
			dbErr = a.db.QueryRow(ctx, `SELECT EXISTS (
				SELECT 1 FROM workspaces WHERE slug = $1 AND deleted_at IS NULL AND id IS DISTINCT FROM $2)`,
				s, current).Scan(&taken)
			if taken {
				return "Workspace with this slug already exists."
			}
			return ""
		},
	}})
	if dbErr != nil {
		return nil, dbErr
	}
	if ok {
		set.add("slug", *slug)
	}
	if s, ok := v.Char("organization_size", drf.CharField{MaxLength: 20, AllowBlank: true, AllowNull: true}); ok {
		set.add("organization_size", s)
	}
	if s, ok := v.Char("logo", drf.CharField{AllowBlank: true, AllowNull: true}); ok {
		set.add("logo", s)
	}
	id, ok, err := v.PK("logo_asset", true, a.liveRowExists(ctx, "file_assets"))
	if err != nil {
		return nil, err
	}
	if ok {
		set.add("logo_asset_id", id)
	}
	if s, ok := v.Choice("timezone", userTimezones, drf.ChoiceField{}); ok {
		set.add("timezone", *s)
	}
	if s, ok := v.Char("background_color", drf.CharField{MaxLength: 255}); ok {
		set.add("background_color", *s)
	}
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	return &set, nil
}

// liveRowExists checks a PrimaryKeyRelatedField queryset of a
// soft-deletable model (Model.objects hides deleted rows).
func (a *API) liveRowExists(ctx context.Context, table string) func(uuid.UUID) (bool, error) {
	return func(id uuid.UUID) (bool, error) {
		var ok bool
		err := a.db.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE id = $1 AND deleted_at IS NULL)`,
			pgx.Identifier{table}.Sanitize()), id).Scan(&ok)
		return ok, err
	}
}

// hasAlphanumeric is plane.utils.content_validator.has_alphanumeric.
func hasAlphanumeric(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) >= 0
}

// pyLen is len(value) for list/dict/str request values; ok is false where
// Python raises TypeError.
func pyLen(v drf.Value) (int, bool) {
	switch v.Kind() {
	case '"':
		return len([]rune(v.Str())), true
	case '[':
		elems, _ := v.Elems()
		return len(elems), true
	case '{':
		var m map[string]any
		if drf.Decode(v.Raw(), &m) != nil {
			return 0, false
		}
		return len(m), true
	}
	return 0, false
}

// createWorkspace ports WorkSpaceViewSet.create. The workspace_seed task
// it enqueues (demo project, issues, cycles, pages...) is not ported.
func (a *API) createWorkspace(c *httpx.Ctx) error {
	if a.cfg.DisableWorkspaceCreation {
		return httpx.Err(http.StatusForbidden, "Workspace creation is not allowed")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list
	}
	name, hasName := data.Get("name")
	slug, hasSlug := data.Get("slug")
	if !hasName || !hasSlug || !drf.PyTruthy(name) || !drf.PyTruthy(slug) {
		return httpx.Err(http.StatusBadRequest, "Both name and slug are required")
	}
	nameLen, ok := pyLen(name)
	if !ok {
		return errViewCrash
	}
	tooLong := nameLen > 80
	if !tooLong {
		slugLen, ok := pyLen(slug)
		if !ok {
			return errViewCrash
		}
		tooLong = slugLen > 48
	}
	if tooLong {
		return httpx.Err(http.StatusBadRequest, "The maximum length for name is 80 and for slug is 48")
	}
	if !name.IsString() {
		return errViewCrash // contains_url() calls str methods
	}
	if containsURL(name.Str()) {
		return httpx.Err(http.StatusBadRequest, "Name cannot contain a URL")
	}

	ctx := c.Context()
	v := drf.NewValidator(data, c.Loc())
	set, err := a.validateWorkspace(ctx, v, nil, false)
	if err != nil {
		return err
	}
	if err := v.Err(); err != nil {
		return err
	}
	if !slices.Contains(set.cols, "background_color") {
		set.add("background_color", randomColor())
	}
	set.add("owner_id", c.User.ID)
	set.add("created_by_id", c.User.ID)

	var companyRole any = ""
	if cr, ok := data.Get("company_role"); ok && !cr.IsNull() {
		companyRole = drf.PyStr(cr) // TextField.to_python: str(value)
	} else if ok {
		companyRole = nil
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var id uuid.UUID
	if err := tx.QueryRow(ctx, set.insertSQL("workspaces"), set.args...).Scan(&id); err != nil {
		if db.IsIntegrityError(err) {
			return httpx.Body(http.StatusConflict, map[string]any{"slug": "The workspace with the slug already exists"})
		}
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO workspace_members (workspace_id, member_id, role, company_role, created_by_id)
		VALUES ($1, $2, 20, $3, $2)`, id, c.User.ID, companyRole); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	w, err := scanWorkspace(a.db.QueryRow(ctx, `SELECT `+workspaceCols+` `+workspaceFrom+` WHERE w.id = $1`, id))
	if err != nil {
		return err
	}
	var total int
	if err := a.db.QueryRow(ctx,
		`SELECT count(*) FROM workspace_members WHERE workspace_id = $1 AND deleted_at IS NULL`, id).Scan(&total); err != nil {
		return err
	}
	role := roleAdmin
	w.TotalMembers, w.Role = &total, &role
	return c.JSON(http.StatusCreated, w)
}

// updateWorkspace ports WorkSpaceViewSet.partial_update (and the stock
// ModelViewSet.update for PUT).
func (a *API) updateWorkspace(partial bool) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		w, err := a.workspaceObject(c)
		if err != nil {
			return err
		}
		data, err := drf.Parse(c.R)
		if err != nil {
			return err
		}
		ctx := c.Context()
		v := drf.NewValidator(data, c.Loc())
		set, err := a.validateWorkspace(ctx, v, &w.ID, partial)
		if err != nil {
			return err
		}
		if err := v.Err(); err != nil {
			return err
		}
		set.add("updated_at", time.Now())
		set.add("updated_by_id", c.User.ID)
		if _, err := a.db.Exec(ctx, `UPDATE workspaces SET `+set.sql()+` WHERE id = $1`,
			append([]any{w.ID}, set.args...)...); err != nil {
			return err
		}
		updated, err := scanWorkspace(a.db.QueryRow(ctx, `SELECT `+workspaceCols+` `+workspaceFrom+` WHERE w.id = $1`, w.ID))
		if err != nil {
			return err
		}
		updated.TotalMembers = w.TotalMembers
		return c.JSON(http.StatusOK, updated)
	}
}

// deleteWorkspace ports WorkSpaceViewSet.destroy and Workspace.delete():
// clear last_workspace_id, soft-delete with cascade, then free the slug by
// suffixing the deletion epoch. Like Django it runs without a transaction.
func (a *API) deleteWorkspace(c *httpx.Ctx) error {
	w, err := a.workspaceObject(c)
	if err != nil {
		return err
	}
	ctx := c.Context()
	if _, err := a.db.Exec(ctx, `UPDATE profiles SET last_workspace_id = NULL WHERE last_workspace_id = $1`, w.ID); err != nil {
		return err
	}
	if err := softdelete.Row(ctx, a.db, "workspaces", w.ID, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `
		UPDATE workspaces SET slug = slug || '__' || trunc(extract(epoch FROM deleted_at))::bigint WHERE id = $1`, w.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// restrictedWorkspaceSlugs is plane.utils.constants.RESTRICTED_WORKSPACE_SLUGS.
var restrictedWorkspaceSlugs = []string{
	"404", "accounts", "api", "create-workspace", "god-mode", "installations", "invitations", "onboarding",
	"profile", "spaces", "workspace-invitations", "password", "flags", "monitor", "monitoring", "ingest",
	"plane-pro", "plane-ultimate", "enterprise", "plane-enterprise", "disco", "silo", "chat", "calendar",
	"drive", "channels", "upgrade", "billing", "sign-in", "sign-up", "signin", "signup", "config", "live",
	"admin", "m", "import", "importers", "integrations", "integration", "configuration", "initiatives",
	"initiative", "workflow", "workflows", "epics", "epic", "story", "mobile", "dashboard", "desktop",
	"onload", "real-time", "one", "pages", "business", "pro", "settings", "license", "licenses",
	"instances", "instance",
}
