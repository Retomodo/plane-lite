package api

import (
	"context"
	"errors"
	"math/big"
	"net/http"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// userLite is UserLiteSerializer; the admin variant (UserAdminLiteSerializer)
// adds email and last_login_medium.
type userLite struct {
	ID          uuid.UUID `json:"id"`
	FirstName   string    `json:"first_name"`
	LastName    string    `json:"last_name"`
	Avatar      string    `json:"avatar"`
	AvatarURL   *string   `json:"avatar_url"`
	IsBot       bool      `json:"is_bot"`
	DisplayName string    `json:"display_name"`
}

type userAdminLite struct {
	userLite        `json:",inline"`
	Email           *string `json:"email"`
	LastLoginMedium string  `json:"last_login_medium"`
}

// workspaceMember is WorkSpaceMemberSerializer / WorkspaceMemberAdminSerializer
// (member nested) and WorkspaceMemberMeSerializer (member as a pk, plus
// draft_issue_count).
type workspaceMember struct {
	ID                      uuid.UUID      `json:"id"`
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
	DeletedAt               *time.Time     `json:"deleted_at"`
	CreatedBy               *uuid.UUID     `json:"created_by"`
	UpdatedBy               *uuid.UUID     `json:"updated_by"`
	Workspace               uuid.UUID      `json:"workspace"`
	Member                  any            `json:"member"`
	Role                    int            `json:"role"`
	CompanyRole             *string        `json:"company_role"`
	ViewProps               jsontext.Value `json:"view_props"`
	DefaultProps            jsontext.Value `json:"default_props"`
	IssueProps              jsontext.Value `json:"issue_props"`
	IsActive                bool           `json:"is_active"`
	GettingStartedChecklist jsontext.Value `json:"getting_started_checklist"`
	Tips                    jsontext.Value `json:"tips"`
	ExploredFeatures        jsontext.Value `json:"explored_features"`
	DraftIssueCount         *int           `json:"draft_issue_count,omitzero"`
}

const memberCols = `wm.id, wm.created_at, wm.updated_at, wm.deleted_at, wm.created_by_id, wm.updated_by_id,
		wm.workspace_id, wm.role, wm.company_role, wm.view_props::text, wm.default_props::text,
		wm.issue_props::text, wm.is_active, wm.getting_started_checklist::text, wm.tips::text,
		wm.explored_features::text, u.id, u.first_name, u.last_name, u.avatar, u.is_bot, u.display_name,
		u.email, u.last_login_medium, fa.id, fa.entity_type`

const memberFrom = `
	FROM workspace_members wm
	JOIN workspaces w ON w.id = wm.workspace_id
	JOIN users u ON u.id = wm.member_id
	LEFT JOIN file_assets fa ON fa.id = u.avatar_asset_id`

const memberSelect = "SELECT " + memberCols + memberFrom

// scanMember reads memberCols (then any extra columns); admin picks the
// nested user serializer.
func scanMember(row pgx.Row, admin bool, extra ...any) (*workspaceMember, error) {
	var (
		m                                           workspaceMember
		u                                           userAdminLite
		view, def, issue, checklist, tips, explored string
		avatarID                                    *uuid.UUID
		avatarType                                  *string
	)
	dest := append([]any{&m.ID, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt, &m.CreatedBy, &m.UpdatedBy,
		&m.Workspace, &m.Role, &m.CompanyRole, &view, &def, &issue, &m.IsActive, &checklist, &tips, &explored,
		&u.ID, &u.FirstName, &u.LastName, &u.Avatar, &u.IsBot, &u.DisplayName, &u.Email, &u.LastLoginMedium,
		&avatarID, &avatarType}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	m.ViewProps, m.DefaultProps, m.IssueProps = jsontext.Value(view), jsontext.Value(def), jsontext.Value(issue)
	m.GettingStartedChecklist, m.Tips, m.ExploredFeatures = jsontext.Value(checklist), jsontext.Value(tips), jsontext.Value(explored)
	u.AvatarURL = imageURL(avatarID, avatarType, &u.Avatar)
	if admin {
		m.Member = u
	} else {
		m.Member = u.userLite
	}
	return &m, nil
}

// queryMembers lists workspace members (newest first, the model ordering).
func (a *API) queryMembers(ctx context.Context, admin bool, where string, args ...any) ([]*workspaceMember, error) {
	rows, err := a.db.Query(ctx, memberSelect+" WHERE "+where+" ORDER BY wm.created_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*workspaceMember{}
	for rows.Next() {
		m, err := scanMember(rows, admin)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (a *API) loadMember(ctx context.Context, id uuid.UUID, admin bool) (*workspaceMember, error) {
	return scanMember(a.db.QueryRow(ctx, memberSelect+" WHERE wm.id = $1", id), admin)
}

// listMembers ports WorkSpaceMemberViewSet.list. Inactive members are
// included; admins and members see emails, guests don't.
func (a *API) listMembers(c *httpx.Ctx) error {
	ctx := c.Context()
	role, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	where := "w.slug = $1 AND wm.deleted_at IS NULL"
	args := []any{c.Param("slug")}
	// SearchFilter over member__display_name and member__first_name.
	for _, term := range drf.SearchTerms(c.Query("search")) {
		args = append(args, "%"+likeEscape(term)+"%")
		n := len(args)
		where += " AND (u.display_name ILIKE $" + itoa(n) + " OR u.first_name ILIKE $" + itoa(n) + ")"
	}
	members, err := a.queryMembers(ctx, role > roleGuest, where, args...)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, members)
}

// getMember ports WorkSpaceMemberViewSet.retrieve.
func (a *API) getMember(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	role, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
	if err != nil {
		return err
	}
	m, err := scanMember(a.db.QueryRow(ctx, memberSelect+
		" WHERE w.slug = $1 AND wm.id = $2 AND wm.deleted_at IS NULL", c.Param("slug"), pk), role > roleGuest)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "Workspace member not found")
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

// activeMember is WorkspaceMember.objects.get(pk=pk, workspace__slug=slug,
// member__is_bot=False, is_active=True): (id, member id).
func (a *API) activeMember(ctx context.Context, slug string, pk uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	var id, member uuid.UUID
	err := a.db.QueryRow(ctx, `
		SELECT wm.id, wm.member_id FROM workspace_members wm
		JOIN workspaces w ON w.id = wm.workspace_id JOIN users u ON u.id = wm.member_id
		WHERE wm.id = $1 AND w.slug = $2 AND NOT u.is_bot AND wm.is_active AND wm.deleted_at IS NULL`,
		pk, slug).Scan(&id, &member)
	return id, member, err
}

// patchMember ports WorkSpaceMemberViewSet.partial_update.
func (a *API) patchMember(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	id, memberID, err := a.activeMember(ctx, slug, pk)
	if err != nil {
		return err
	}
	if memberID == c.User.ID {
		return httpx.Err(http.StatusBadRequest, "You cannot update your own role")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	// A guest can't hold a higher role in any project. This runs before
	// validation, as in Django.
	if r, ok := data.Get("role"); ok && data.IsDict() {
		n, ok := drf.PyInt(r)
		if !ok {
			return errViewCrash
		}
		if n.Cmp(big.NewInt(roleGuest)) == 0 {
			if _, err := a.db.Exec(ctx, `
				UPDATE project_members pm SET role = 5 FROM workspaces w
				WHERE w.id = pm.workspace_id AND w.slug = $1 AND pm.member_id = $2 AND pm.deleted_at IS NULL`,
				slug, memberID); err != nil {
				return err
			}
		}
	}
	v := drf.NewValidator(data, c.Loc())
	var set setList
	if s, ok := v.Choice("role", roleChoices, drf.ChoiceField{}); ok {
		set.add("role", atoi(*s))
	}
	if s, ok := v.Char("company_role", drf.CharField{AllowBlank: true, AllowNull: true}); ok {
		set.add("company_role", s)
	}
	for _, col := range []string{"view_props", "default_props", "issue_props", "getting_started_checklist", "tips", "explored_features"} {
		if raw, ok := v.JSON(col, false); ok {
			set.addCast(col, string(raw), "::jsonb")
		}
	}
	if b, ok := v.Bool("is_active"); ok {
		set.add("is_active", b)
	}
	ws, ok, err := v.PK("workspace", false, a.liveRowExists(ctx, "workspaces"))
	if err != nil {
		return err
	}
	if ok {
		set.add("workspace_id", ws)
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
	if _, err := a.db.Exec(ctx, `UPDATE workspace_members SET `+set.sql()+` WHERE id = $1`,
		append([]any{id}, set.args...)...); err != nil {
		return err
	}
	m, err := a.loadMember(ctx, id, false)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, m)
}

// soleProjectAdmin is the "only admin of some project" guard: a project of
// the workspace with exactly one member row (any state) that is this
// member_id as admin.
func (a *API) soleProjectAdmin(ctx context.Context, slug string, memberID uuid.UUID) (bool, error) {
	var found bool
	err := a.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM projects p JOIN workspaces w ON w.id = p.workspace_id
			WHERE w.slug = $1 AND p.deleted_at IS NULL
				AND (SELECT count(*) FROM project_members pm WHERE pm.project_id = p.id) = 1
				AND (SELECT count(*) FROM project_members pm
					WHERE pm.project_id = p.id AND pm.member_id = $2 AND pm.role = 20) = 1)`,
		slug, memberID).Scan(&found)
	return found, err
}

// deactivateMember turns off a workspace membership and the member's
// project memberships in that workspace.
func (a *API) deactivateMember(ctx context.Context, slug string, id, memberID, actor uuid.UUID) error {
	if _, err := a.db.Exec(ctx, `
		UPDATE project_members pm SET is_active = false, updated_at = now() FROM workspaces w
		WHERE w.id = pm.workspace_id AND w.slug = $1 AND pm.member_id = $2 AND pm.is_active AND pm.deleted_at IS NULL`,
		slug, memberID); err != nil {
		return err
	}
	_, err := a.db.Exec(ctx, `UPDATE workspace_members SET is_active = false, updated_at = now(), updated_by_id = $2 WHERE id = $1`, id, actor)
	return err
}

// deleteMember ports WorkSpaceMemberViewSet.destroy.
func (a *API) deleteMember(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	id, memberID, err := a.activeMember(ctx, slug, pk)
	if err != nil {
		return err
	}
	var requesterID uuid.UUID
	var requesterRole, targetRole int
	if err := a.db.QueryRow(ctx, `
		SELECT wm.id, wm.role, (SELECT role FROM workspace_members WHERE id = $3)
		FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
		WHERE w.slug = $1 AND wm.member_id = $2 AND wm.is_active AND wm.deleted_at IS NULL`,
		slug, c.User.ID, id).Scan(&requesterID, &requesterRole, &targetRole); err != nil {
		return err
	}
	if requesterID == id {
		return httpx.Err(http.StatusBadRequest, "You cannot remove yourself from the workspace. Please use leave workspace")
	}
	if requesterRole < targetRole {
		return httpx.Err(http.StatusBadRequest, "You cannot remove a user having role higher than you")
	}
	// Django compares project member ids with the WorkspaceMember's own id
	// here, so the guard only fires on an id collision.
	sole, err := a.soleProjectAdmin(ctx, slug, id)
	if err != nil {
		return err
	}
	if sole {
		return httpx.Err(http.StatusBadRequest, "User is a part of some projects where they are the only admin, they should either leave that project or promote another user to admin.")
	}
	if err := a.deactivateMember(ctx, slug, id, memberID, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// leaveWorkspace ports WorkSpaceMemberViewSet.leave.
func (a *API) leaveWorkspace(c *httpx.Ctx) error {
	ctx := c.Context()
	slug := c.Param("slug")
	var (
		id     uuid.UUID
		role   int
		admins int
	)
	if err := a.db.QueryRow(ctx, `
		SELECT wm.id, wm.role, (
			SELECT count(*) FROM workspace_members o
			WHERE o.workspace_id = wm.workspace_id AND o.role = 20 AND o.is_active AND o.deleted_at IS NULL)
		FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
		WHERE w.slug = $1 AND wm.member_id = $2 AND wm.is_active AND wm.deleted_at IS NULL`,
		slug, c.User.ID).Scan(&id, &role, &admins); err != nil {
		return err
	}
	if role == roleAdmin && admins <= 1 {
		return httpx.Err(http.StatusBadRequest, "You cannot leave the workspace as you are the only admin of the workspace you will have to either delete the workspace or promote another user to admin.")
	}
	sole, err := a.soleProjectAdmin(ctx, slug, c.User.ID)
	if err != nil {
		return err
	}
	if sole {
		return httpx.Err(http.StatusBadRequest, "You are a part of some projects where you are the only admin, you should either leave the project or promote another user to admin.")
	}
	if err := a.deactivateMember(ctx, slug, id, c.User.ID, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}

// noMembership is WorkspaceMemberMeSerializer(None).data: the serializer's
// initial values for its writable fields.
var noMembership = map[string]any{
	"company_role": "", "created_by": nil, "default_props": nil, "deleted_at": nil,
	"explored_features": nil, "getting_started_checklist": nil, "is_active": false, "issue_props": nil,
	"member": nil, "role": nil, "tips": nil, "updated_by": nil, "view_props": nil, "workspace": nil,
}

// workspaceMemberMe ports WorkspaceMemberUserEndpoint.get.
func (a *API) workspaceMemberMe(c *httpx.Ctx) error {
	ctx := c.Context()
	var drafts int
	m, err := scanMember(a.db.QueryRow(ctx, `SELECT `+memberCols+`,
			(SELECT count(*) FROM draft_issues d
			WHERE d.created_by_id = $2 AND d.workspace_id = wm.workspace_id AND d.deleted_at IS NULL)
		`+memberFrom+`
		WHERE w.slug = $1 AND wm.member_id = $2 AND wm.is_active AND wm.deleted_at IS NULL
		ORDER BY wm.created_at DESC LIMIT 1`, c.Param("slug"), c.User.ID), false, &drafts)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.JSON(http.StatusOK, noMembership)
	}
	if err != nil {
		return err
	}
	m.Member, m.DraftIssueCount = m.Member.(userLite).ID, &drafts
	return c.JSON(http.StatusOK, m)
}
