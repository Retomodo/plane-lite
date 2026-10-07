package api

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/mail"
)

// projectLite is ProjectLiteSerializer.
type projectLite struct {
	ID            uuid.UUID      `json:"id"`
	Identifier    string         `json:"identifier"`
	Name          string         `json:"name"`
	CoverImage    *string        `json:"cover_image"`
	CoverImageURL *string        `json:"cover_image_url"`
	LogoProps     jsontext.Value `json:"logo_props"`
	Description   string         `json:"description"`
}

// projectMember is ProjectMemberSerializer (member as UserLite) and
// ProjectMemberAdminSerializer (UserAdminLite).
type projectMember struct {
	ID           uuid.UUID      `json:"id"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    *time.Time     `json:"deleted_at"`
	CreatedBy    *uuid.UUID     `json:"created_by"`
	UpdatedBy    *uuid.UUID     `json:"updated_by"`
	Workspace    workspaceLite  `json:"workspace"`
	Project      projectLite    `json:"project"`
	Member       any            `json:"member"`
	Comment      *string        `json:"comment"`
	Role         int            `json:"role"`
	ViewProps    jsontext.Value `json:"view_props"`
	DefaultProps jsontext.Value `json:"default_props"`
	Preferences  jsontext.Value `json:"preferences"`
	SortOrder    float64        `json:"sort_order"`
	IsActive     bool           `json:"is_active"`
}

const projectMemberSelect = `
	SELECT pm.id, pm.created_at, pm.updated_at, pm.deleted_at, pm.created_by_id, pm.updated_by_id,
		pm.comment, pm.role, pm.view_props::text, pm.default_props::text, pm.preferences::text, pm.sort_order,
		pm.is_active, w.id, w.name, w.slug, w.logo, w.logo_asset_id, wa.entity_type,
		p.id, p.identifier, p.name, p.cover_image, p.cover_image_asset_id, ca.entity_type, p.logo_props::text,
		p.description, u.id, u.first_name, u.last_name, u.avatar, u.is_bot, u.display_name, u.email,
		u.last_login_medium, ua.id, ua.entity_type
	FROM project_members pm
	JOIN workspaces w ON w.id = pm.workspace_id
	LEFT JOIN file_assets wa ON wa.id = w.logo_asset_id
	JOIN projects p ON p.id = pm.project_id
	LEFT JOIN file_assets ca ON ca.id = p.cover_image_asset_id
	JOIN users u ON u.id = pm.member_id
	LEFT JOIN file_assets ua ON ua.id = u.avatar_asset_id`

func scanProjectMember(row pgx.Row, admin bool) (*projectMember, error) {
	var (
		m                         projectMember
		u                         userAdminLite
		view, def, prefs, logo    string
		wsLogo, wsType, coverType *string
		wsLogoID, coverID         *uuid.UUID
		avatarID                  *uuid.UUID
		avatarType                *string
	)
	if err := row.Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt, &m.CreatedBy, &m.UpdatedBy,
		&m.Comment, &m.Role, &view, &def, &prefs, &m.SortOrder,
		&m.IsActive, &m.Workspace.ID, &m.Workspace.Name, &m.Workspace.Slug, &wsLogo, &wsLogoID, &wsType,
		&m.Project.ID, &m.Project.Identifier, &m.Project.Name, &m.Project.CoverImage, &coverID, &coverType, &logo,
		&m.Project.Description, &u.ID, &u.FirstName, &u.LastName, &u.Avatar, &u.IsBot, &u.DisplayName, &u.Email,
		&u.LastLoginMedium, &avatarID, &avatarType); err != nil {
		return nil, err
	}
	m.ViewProps, m.DefaultProps, m.Preferences = jsontext.Value(view), jsontext.Value(def), jsontext.Value(prefs)
	m.Workspace.LogoURL = imageURL(wsLogoID, wsType, wsLogo)
	m.Project.CoverImageURL = imageURL(coverID, coverType, m.Project.CoverImage)
	m.Project.LogoProps = jsontext.Value(logo)
	u.AvatarURL = imageURL(avatarID, avatarType, &u.Avatar)
	if admin {
		m.Member = u
	} else {
		m.Member = u.userLite
	}
	return &m, nil
}

func (a *API) loadProjectMember(ctx context.Context, id uuid.UUID, admin bool) (*projectMember, error) {
	return scanProjectMember(a.db.QueryRow(ctx, projectMemberSelect+" WHERE pm.id = $1", id), admin)
}

// projectMemberRole is ProjectMemberRoleSerializer (whose fields= argument
// DynamicBaseSerializer ignores). Project is dropped where a view pops it.
type projectMemberRole struct {
	ID           uuid.UUID  `json:"id"`
	Role         int        `json:"role"`
	Member       *uuid.UUID `json:"member"`
	Project      *uuid.UUID `json:"project,omitzero"`
	OriginalRole int        `json:"original_role"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (a *API) queryMemberRoles(ctx context.Context, sql string, args ...any) ([]*projectMemberRole, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*projectMemberRole{}
	for rows.Next() {
		var m projectMemberRole
		if err := rows.Scan(&m.ID, &m.Role, &m.Member, &m.Project, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.OriginalRole = m.Role
		out = append(out, &m)
	}
	return out, rows.Err()
}

// listProjectMembers ports ProjectMemberViewSet.list: active, non-bot
// members who are also active in the workspace. ?search= is not applied.
func (a *API) listProjectMembers(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	members, err := a.queryMemberRoles(c.Context(), `
		SELECT pm.id, pm.role, pm.member_id, pm.project_id, pm.created_at
		FROM project_members pm
		JOIN workspaces w ON w.id = pm.workspace_id
		JOIN users u ON u.id = pm.member_id
		JOIN workspace_members wm ON wm.member_id = u.id
		JOIN workspaces mw ON mw.id = wm.workspace_id
		WHERE pm.project_id = $1 AND w.slug = $2 AND NOT u.is_bot AND pm.is_active AND pm.deleted_at IS NULL
			AND mw.slug = $2 AND wm.is_active
		ORDER BY pm.created_at DESC`, projectID, c.Param("slug"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, members)
}

// createProjectMembers ports ProjectMemberViewSet.create: add workspace
// members to the project (reactivating earlier memberships) and email each.
func (a *API) createProjectMembers(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var workspaceID uuid.UUID
	if _, workspaceID, _, _, err = a.projectObject(ctx, slug, projectID); err != nil {
		return err
	}
	var entries []drf.Value
	if v, ok := data.Get("members"); ok {
		n, ok := pyLen(v)
		if !ok {
			return errViewCrash
		}
		if n > 0 {
			if entries, ok = v.Elems(); !ok {
				return errViewCrash // a str or dict iterates without .get()
			}
		}
	}
	if len(entries) == 0 {
		return httpx.Err(http.StatusBadRequest, "At least one member is required")
	}

	// member_roles = {member_id: role}: later entries win.
	type entry struct {
		raw     drf.Value // member_id as sent
		id      uuid.UUID
		role    drf.Value
		hasRole bool
	}
	parsed := make([]entry, len(entries))
	roleOf := map[string]drf.Value{} // keyed by the JSON of member_id
	var order []string
	for i, e := range entries {
		if e.Kind() != '{' {
			return errViewCrash
		}
		mid, _ := e.Member("member_id")
		if mid.Raw() == nil {
			mid = drf.JSONValue(jsontext.Value("null"))
		}
		role, ok := e.Member("role")
		if !ok {
			role = drf.JSONValue(jsontext.Value("null"))
		}
		parsed[i] = entry{raw: mid, role: role, hasRole: ok}
		key := string(mid.Raw())
		if mid.Kind() == '[' || mid.Kind() == '{' {
			return errViewCrash // unhashable dict key
		}
		if _, seen := roleOf[key]; !seen {
			order = append(order, key)
		}
		roleOf[key] = role
	}
	// The workspace role caps the project role, checked per distinct member.
	for _, key := range order {
		mid := drf.JSONValue(jsontext.Value(key))
		var wsRole int
		if mid.IsNull() {
			return pgx.ErrNoRows // member=None matches nothing
		}
		id, ok := drf.UUIDValue(mid)
		if !ok {
			return errInvalidDetail
		}
		if err := a.db.QueryRow(ctx, `
			SELECT wm.role FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
			WHERE w.slug = $1 AND wm.member_id = $2 AND wm.is_active AND wm.deleted_at IS NULL`, slug, id).Scan(&wsRole); err != nil {
			return err
		}
		role := roleOf[key]
		if wsRole == roleAdmin && pyEqualsAny(role, 5, 15) {
			return httpx.Err(http.StatusBadRequest, "You cannot add a user with role lower than the workspace role")
		}
		if wsRole == roleGuest && pyEqualsAny(role, 15, 20) {
			return httpx.Err(http.StatusBadRequest, "You cannot add a user with role higher than the workspace role")
		}
	}
	ids := make([]uuid.UUID, len(parsed))
	for i := range parsed {
		parsed[i].id, _ = drf.UUIDValue(parsed[i].raw)
		ids[i] = parsed[i].id
	}

	// Existing memberships come back with the requested role, looked up by
	// str(member_id) among the keys as sent.
	rows, err := a.db.Query(ctx, `SELECT id, member_id FROM project_members
		WHERE project_id = $1 AND member_id = ANY($2) AND deleted_at IS NULL`, projectID, ids)
	if err != nil {
		return err
	}
	type existing struct{ ID, Member uuid.UUID }
	found, err := pgx.CollectRows(rows, pgx.RowToStructByPos[existing])
	if err != nil {
		return err
	}
	for _, ex := range found {
		role, ok := roleOf[string(jsonString(ex.Member.String()))]
		if !ok {
			return errKeyMissing
		}
		n, err := modelInt(role)
		if err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, `UPDATE project_members SET is_active = true, role = $2 WHERE id = $1`, ex.ID, n); err != nil {
			return err
		}
	}

	// New rows (bulk_create, so no created_by and no save()): each member's
	// properties sort above their other projects in the workspace.
	for _, e := range parsed {
		role := e.role // member.get("role", 5)
		if !e.hasRole {
			role = drf.JSONValue(jsontext.Value("5"))
		}
		n, err := modelInt(role)
		if err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, `
			INSERT INTO project_members (member_id, role, project_id, workspace_id, created_at, updated_at)
			VALUES ($1, $2, $3, $4, clock_timestamp(), clock_timestamp()) ON CONFLICT DO NOTHING`,
			e.id, n, projectID, workspaceID); err != nil {
			return err
		}
	}
	if _, err := a.db.Exec(ctx, `
		INSERT INTO project_user_properties (user_id, project_id, workspace_id, sort_order, created_at, updated_at)
		SELECT m.id, $2, $3, coalesce((
			SELECT min(sort_order) - 10000 FROM project_user_properties
			WHERE workspace_id = $3 AND user_id = m.id AND deleted_at IS NULL), 65535),
			clock_timestamp(), clock_timestamp()
		FROM unnest($1::uuid[]) WITH ORDINALITY AS m(id, n) ORDER BY m.n
		ON CONFLICT DO NOTHING`, ids, projectID, workspaceID); err != nil {
		return err
	}

	members, err := a.queryMemberRoles(ctx, `
		SELECT id, role, member_id, project_id, created_at FROM project_members
		WHERE project_id = $1 AND member_id = ANY($2) AND deleted_at IS NULL ORDER BY created_at DESC`, projectID, ids)
	if err != nil {
		return err
	}
	for _, m := range members {
		if err := jobs.Enqueue(ctx, a.jobs, projectAddUserEmail{
			CurrentSite: a.baseHost(true), ProjectMemberID: m.ID, InviterID: c.User.ID,
		}); err != nil {
			return err
		}
	}
	return c.JSON(http.StatusCreated, members)
}

// errKeyMissing is a KeyError reaching BaseAPIView.
var errKeyMissing = httpx.Err(http.StatusBadRequest, "The required key does not exist.")

// pyEqualsAny is `value in [a, b]` for ints: numbers (and bools) compare by
// value, strings never match.
func pyEqualsAny(v drf.Value, choices ...int64) bool {
	switch v.Kind() {
	case '0', 't', 'f':
		f, ok := drf.PyFloat(v)
		if !ok {
			return false
		}
		for _, c := range choices {
			if f == float64(c) {
				return true
			}
		}
	}
	return false
}

// modelInt is IntegerField.get_prep_value on a raw request value: None
// stays NULL (an IntegrityError on save), anything else goes through int().
func modelInt(v drf.Value) (*int64, error) {
	if v.IsNull() {
		return nil, nil
	}
	n, ok := drf.PyInt(v)
	if !ok {
		return nil, errViewCrash
	}
	if !n.IsInt64() || n.Int64() < -32768 || n.Int64() > 32767 {
		return nil, errViewCrash // smallint out of range: a DataError
	}
	i := n.Int64()
	return &i, nil
}

// getProjectMember ports ProjectMemberViewSet.retrieve: admins and members
// get the full member, guests the role view.
func (a *API) getProjectMember(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	requester, err := a.requestingProjectMember(ctx, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	var id uuid.UUID
	err = a.db.QueryRow(ctx, `
		SELECT pm.id FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id JOIN users u ON u.id = pm.member_id
		WHERE pm.id = $1 AND pm.project_id = $2 AND w.slug = $3 AND NOT u.is_bot AND pm.is_active AND pm.deleted_at IS NULL`,
		pk, projectID, slug).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "Project member not found")
	}
	if err != nil {
		return err
	}
	if requester.role > roleGuest {
		m, err := a.loadProjectMember(ctx, id, true)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, m)
	}
	m, err := a.queryMemberRoles(ctx, `SELECT id, role, member_id, project_id, created_at FROM project_members WHERE id = $1`, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m[0])
}

type memberRow struct {
	id     uuid.UUID
	member *uuid.UUID
	role   int
}

// requestingProjectMember is ProjectMember.objects.get(project_id=...,
// workspace__slug=..., member=user, is_active=True).
func (a *API) requestingProjectMember(ctx context.Context, slug string, projectID, user uuid.UUID) (*memberRow, error) {
	var m memberRow
	err := a.db.QueryRow(ctx, `
		SELECT pm.id, pm.member_id, pm.role FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE pm.project_id = $1 AND w.slug = $2 AND pm.member_id = $3 AND pm.is_active AND pm.deleted_at IS NULL`,
		projectID, slug, user).Scan(&m.id, &m.member, &m.role)
	return &m, err
}

// dataContains is `name in request.data`: a key of a dict, an element of a
// list, a substring of a str.
func dataContains(d *drf.Data, name string) (bool, error) {
	if d.IsDict() {
		return d.Has(name), nil
	}
	root := d.Root()
	switch root.Kind() {
	case '[':
		elems, _ := root.Elems()
		for _, e := range elems {
			if e.IsString() && e.Str() == name {
				return true, nil
			}
		}
		return false, nil
	case '"':
		return strings.Contains(root.Str(), name), nil
	}
	return false, errViewCrash
}

// patchProjectMember ports ProjectMemberViewSet.partial_update. Role and
// is_active changes are for project admins (or workspace admins), and never
// on members at or above the requester's own role.
func (a *API) patchProjectMember(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var target memberRow
	if err := a.db.QueryRow(ctx, `
		SELECT pm.id, pm.member_id, pm.role FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE pm.id = $1 AND w.slug = $2 AND pm.project_id = $3 AND pm.is_active AND pm.deleted_at IS NULL`,
		pk, slug, projectID).Scan(&target.id, &target.member, &target.role); err != nil {
		return err
	}
	wsRoleOf := func(member *uuid.UUID) (int, error) {
		var role int
		err := a.db.QueryRow(ctx, `
			SELECT wm.role FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
			WHERE w.slug = $1 AND wm.member_id = $2 AND wm.is_active AND wm.deleted_at IS NULL`, slug, member).Scan(&role)
		return role, err
	}
	targetWsRole, err := wsRoleOf(target.member)
	if err != nil {
		return err
	}
	requesterWsRole, err := wsRoleOf(&c.User.ID)
	if err != nil {
		return err
	}
	wsAdmin := requesterWsRole == roleAdmin
	if target.member != nil && *target.member == c.User.ID && !wsAdmin {
		return httpx.Err(http.StatusBadRequest, "You cannot update your own role")
	}
	requester, err := a.requestingProjectMember(ctx, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	hasRole, err := dataContains(data, "role")
	if err != nil {
		return err
	}
	if hasRole {
		if requester.role < roleAdmin && !wsAdmin {
			return httpx.Err(http.StatusForbidden, "You do not have permission to update roles")
		}
		if target.role >= requester.role && !wsAdmin {
			return httpx.Err(http.StatusForbidden, "You cannot update the role of a member with a role equal to or higher than your own")
		}
		if !data.IsDict() {
			return errViewCrash // request.data.get
		}
		raw, _ := data.Get("role")
		newRole, ok := drf.PyInt(raw)
		if !ok {
			return errViewCrash
		}
		if newRole.Cmp(big.NewInt(int64(requester.role))) >= 0 && !wsAdmin {
			return httpx.Err(http.StatusForbidden, "You cannot assign a role equal to or higher than your own")
		}
		if targetWsRole == roleGuest && (newRole.Cmp(big.NewInt(15)) == 0 || newRole.Cmp(big.NewInt(20)) == 0) {
			return httpx.Err(http.StatusBadRequest, "You cannot add a user with role higher than the workspace role")
		}
	}
	hasActive, err := dataContains(data, "is_active")
	if err != nil {
		return err
	}
	if hasActive {
		if requester.role < roleAdmin && !wsAdmin {
			return httpx.Err(http.StatusForbidden, "You do not have permission to update member status")
		}
		if target.role >= requester.role && !wsAdmin {
			return httpx.Err(http.StatusForbidden, "You cannot update the status of a member with a role equal to or higher than your own")
		}
	}

	v := drf.NewValidator(data, c.Loc())
	var set setList
	if s, ok := v.Char("comment", drf.CharField{AllowBlank: true, AllowNull: true}); ok {
		set.add("comment", s)
	}
	if s, ok := v.Choice("role", roleChoices, drf.ChoiceField{}); ok {
		set.add("role", atoi(*s))
	}
	for _, col := range []string{"view_props", "default_props", "preferences"} {
		if raw, ok := v.JSON(col, false); ok {
			set.addCast(col, string(raw), "::jsonb")
		}
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
	}
	if b, ok := v.Bool("is_active"); ok {
		set.add("is_active", b)
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
	if _, err := a.db.Exec(ctx, `UPDATE project_members SET `+set.sql()+` WHERE id = $1`,
		append([]any{target.id}, set.args...)...); err != nil {
		return err
	}
	m, err := a.loadProjectMember(ctx, target.id, false)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

// deactivateProjectMember is `member.is_active = False; member.save()`.
func (a *API) deactivateProjectMember(ctx context.Context, id, actor uuid.UUID) error {
	_, err := a.db.Exec(ctx, `UPDATE project_members SET is_active = false, updated_at = now(), updated_by_id = $2 WHERE id = $1`, id, actor)
	return err
}

// deleteProjectMember ports ProjectMemberViewSet.destroy.
func (a *API) deleteProjectMember(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var target memberRow
	if err := a.db.QueryRow(ctx, `
		SELECT pm.id, pm.member_id, pm.role FROM project_members pm
		JOIN workspaces w ON w.id = pm.workspace_id JOIN users u ON u.id = pm.member_id
		WHERE w.slug = $1 AND pm.project_id = $2 AND pm.id = $3 AND NOT u.is_bot AND pm.is_active AND pm.deleted_at IS NULL`,
		slug, projectID, pk).Scan(&target.id, &target.member, &target.role); err != nil {
		return err
	}
	requester, err := a.requestingProjectMember(ctx, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	if target.id == requester.id {
		return httpx.Err(http.StatusBadRequest, "You cannot remove yourself from the workspace. Please use leave workspace")
	}
	if requester.role < target.role {
		return httpx.Err(http.StatusBadRequest, "You cannot remove a user having role higher than you")
	}
	if err := a.deactivateProjectMember(ctx, target.id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// leaveProject ports ProjectMemberViewSet.leave.
func (a *API) leaveProject(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	m, err := a.requestingProjectMember(ctx, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	if m.role == roleAdmin {
		var admins int
		if err := a.db.QueryRow(ctx, `
			SELECT count(*) FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
			WHERE w.slug = $1 AND pm.project_id = $2 AND pm.role = 20 AND pm.is_active AND pm.deleted_at IS NULL`,
			slug, projectID).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return httpx.Err(http.StatusBadRequest, "You cannot leave the project as your the only admin of the project you will have to either delete the project or create an another admin")
		}
	}
	if err := a.deactivateProjectMember(ctx, m.id, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// projectMemberMe ports ProjectMemberUserEndpoint.get.
func (a *API) projectMemberMe(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	m, err := a.requestingProjectMember(ctx, c.Param("slug"), projectID, c.User.ID)
	if err != nil {
		return err
	}
	full, err := a.loadProjectMember(ctx, m.id, false)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, full)
}

// projectRoles ports UserProjectRolesEndpoint.get: {project_id: role} for
// the requester's active memberships.
func (a *API) projectRoles(c *httpx.Ctx) error {
	rows, err := a.db.Query(c.Context(), `
		SELECT pm.project_id::text, pm.role FROM project_members pm
		JOIN workspaces w ON w.id = pm.workspace_id
		JOIN workspace_members wm ON wm.member_id = pm.member_id
		JOIN workspaces mw ON mw.id = wm.workspace_id
		WHERE w.slug = $1 AND pm.member_id = $2 AND pm.is_active AND pm.deleted_at IS NULL
			AND mw.slug = $1 AND wm.is_active`, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var (
			project string
			role    int
		)
		if err := rows.Scan(&project, &role); err != nil {
			return err
		}
		out[project] = role
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// workspaceProjectMembers ports WorkspaceProjectMemberEndpoint.get: the
// members of every project the requester is in, grouped by project.
func (a *API) workspaceProjectMembers(c *httpx.Ctx) error {
	members, err := a.queryMemberRoles(c.Context(), `
		SELECT pm.id, pm.role, pm.member_id, pm.project_id, pm.created_at
		FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE w.slug = $1 AND pm.is_active AND pm.deleted_at IS NULL AND pm.project_id IN (
			SELECT project_id FROM project_members WHERE member_id = $2 AND is_active AND deleted_at IS NULL)
		ORDER BY pm.created_at DESC`, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	out := map[string][]*projectMemberRole{}
	for _, m := range members {
		key := m.Project.String()
		m.Project = nil
		out[key] = append(out[key], m)
	}
	return c.JSON(http.StatusOK, out)
}

// projectUserProperty is ProjectUserPropertySerializer.
type projectUserProperty struct {
	ID                uuid.UUID      `json:"id"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         *time.Time     `json:"deleted_at"`
	CreatedBy         *uuid.UUID     `json:"created_by"`
	UpdatedBy         *uuid.UUID     `json:"updated_by"`
	Filters           jsontext.Value `json:"filters"`
	DisplayFilters    jsontext.Value `json:"display_filters"`
	DisplayProperties jsontext.Value `json:"display_properties"`
	RichFilters       jsontext.Value `json:"rich_filters"`
	Preferences       jsontext.Value `json:"preferences"`
	SortOrder         float64        `json:"sort_order"`
	User              uuid.UUID      `json:"user"`
	Workspace         uuid.UUID      `json:"workspace"`
	Project           uuid.UUID      `json:"project"`
}

// projectUserPropertiesFor is ProjectUserProperty.objects.get_or_create(
// user=..., project_id=...).
func (a *API) projectUserPropertiesFor(ctx context.Context, projectID, user uuid.UUID) (*projectUserProperty, error) {
	load := func() (*projectUserProperty, error) {
		var (
			p                                   projectUserProperty
			filters, display, props, rich, pref string
		)
		err := a.db.QueryRow(ctx, `
			SELECT id, created_at, updated_at, deleted_at, created_by_id, updated_by_id, filters::text,
				display_filters::text, display_properties::text, rich_filters::text, preferences::text,
				sort_order, user_id, workspace_id, project_id
			FROM project_user_properties WHERE user_id = $1 AND project_id = $2 AND deleted_at IS NULL`, user, projectID).
			Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.CreatedBy, &p.UpdatedBy, &filters,
				&display, &props, &rich, &pref, &p.SortOrder, &p.User, &p.Workspace, &p.Project)
		p.Filters, p.DisplayFilters, p.DisplayProperties = jsontext.Value(filters), jsontext.Value(display), jsontext.Value(props)
		p.RichFilters, p.Preferences = jsontext.Value(rich), jsontext.Value(pref)
		return &p, err
	}
	p, err := load()
	if !errors.Is(err, pgx.ErrNoRows) {
		return p, err
	}
	if _, err := a.db.Exec(ctx, `
		INSERT INTO project_user_properties (user_id, project_id, workspace_id, created_by_id)
		SELECT $1, id, workspace_id, $1 FROM projects WHERE id = $2
		ON CONFLICT DO NOTHING`, user, projectID); err != nil {
		return nil, err
	}
	return load()
}

// getProjectUserProperties ports ProjectUserDisplayPropertyEndpoint.get.
func (a *API) getProjectUserProperties(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	p, err := a.projectUserPropertiesFor(c.Context(), projectID, c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

// patchProjectUserProperties ports ProjectUserDisplayPropertyEndpoint.patch.
func (a *API) patchProjectUserProperties(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	p, err := a.projectUserPropertiesFor(ctx, projectID, c.User.ID)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	for _, col := range []string{"filters", "display_filters", "display_properties", "rich_filters", "preferences"} {
		if raw, ok := v.JSON(col, false); ok {
			set.addCast(col, string(raw), "::jsonb")
		}
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
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
	if _, err := a.db.Exec(ctx, `UPDATE project_user_properties SET `+set.sql()+` WHERE id = $1`,
		append([]any{p.ID}, set.args...)...); err != nil {
		return err
	}
	// Read back by id: the row may now carry a deleted_at.
	var updated projectUserProperty
	var filters, display, props, rich, pref string
	if err := a.db.QueryRow(ctx, `
		SELECT id, created_at, updated_at, deleted_at, created_by_id, updated_by_id, filters::text,
			display_filters::text, display_properties::text, rich_filters::text, preferences::text,
			sort_order, user_id, workspace_id, project_id
		FROM project_user_properties WHERE id = $1`, p.ID).
		Scan(&updated.ID, &updated.CreatedAt, &updated.UpdatedAt, &updated.DeletedAt, &updated.CreatedBy, &updated.UpdatedBy,
			&filters, &display, &props, &rich, &pref, &updated.SortOrder, &updated.User, &updated.Workspace, &updated.Project); err != nil {
		return err
	}
	updated.Filters, updated.DisplayFilters, updated.DisplayProperties = jsontext.Value(filters), jsontext.Value(display), jsontext.Value(props)
	updated.RichFilters, updated.Preferences = jsontext.Value(rich), jsontext.Value(pref)
	return c.JSON(http.StatusOK, &updated)
}

// projectAddUserEmail is bgtasks.project_add_user_email_task.project_add_user_email.
type projectAddUserEmail struct {
	CurrentSite     string    `json:"current_site"`
	ProjectMemberID uuid.UUID `json:"project_member_id"`
	InviterID       uuid.UUID `json:"inviter_id"`
}

func (projectAddUserEmail) Kind() string                 { return "project_add_user_email" }
func (projectAddUserEmail) InsertOpts() river.InsertOpts { return emailInsertOpts }

func (a *API) registerProjectJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j projectAddUserEmail) error {
		var inviter, projectName, workspaceName, slug string
		var email *string
		var projectID uuid.UUID
		if err := a.db.QueryRow(ctx, `SELECT first_name FROM users WHERE id = $1`, j.InviterID).Scan(&inviter); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		err := a.db.QueryRow(ctx, `
			SELECT p.id, p.name, w.name, w.slug, u.email FROM project_members pm
			JOIN projects p ON p.id = pm.project_id JOIN workspaces w ON w.id = pm.workspace_id
			JOIN users u ON u.id = pm.member_id
			WHERE pm.id = $1 AND pm.deleted_at IS NULL`, j.ProjectMemberID).
			Scan(&projectID, &projectName, &workspaceName, &slug, &email)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && email == nil) {
			return nil // the membership is gone
		}
		if err != nil {
			return err
		}
		html, err := mail.Render("notifications/project_addition.html", map[string]string{
			"project_name":       projectName,
			"workspace_name":     workspaceName,
			"email":              *email,
			"inviter_first_name": inviter,
			"project_url":        j.CurrentSite + "/" + slug + "/projects/" + projectID.String() + "/issues",
		})
		if err != nil {
			return err
		}
		return a.mailer.Send(ctx, []string{*email}, "You have been invited to a Plane project", mail.PlainText(html), html)
	})
}
