package api

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/httpx"
)

// Plane's role values (app.permissions.ROLE).
const (
	roleAdmin  = 20
	roleMember = 15
	roleGuest  = 5
)

var (
	anyRole     = []int{roleAdmin, roleMember, roleGuest}
	adminMember = []int{roleAdmin, roleMember}
	adminOnly   = []int{roleAdmin}
)

// errNoRole is allow_permission's denial; DRF permission classes deny with
// httpx.ErrForbidden ({"detail": ...}) instead.
var errNoRole = httpx.Err(http.StatusForbidden, "You don't have the required permissions.")

// workspaceRole is the user's role through an active membership of the
// workspace with this slug, or 0. Like Django's workspace__slug joins it
// does not look at the workspace's own deleted_at (deleted workspaces have
// their slug rewritten anyway).
func (a *API) workspaceRole(ctx context.Context, slug string, user uuid.UUID) (int, error) {
	var role int
	err := a.db.QueryRow(ctx, `
		SELECT wm.role FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
		WHERE w.slug = $1 AND wm.member_id = $2 AND wm.is_active AND wm.deleted_at IS NULL
		ORDER BY wm.role DESC LIMIT 1`, slug, user).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return role, err
}

// allowWorkspace is @allow_permission(roles, level="WORKSPACE").
func (a *API) allowWorkspace(roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		role, err := a.workspaceRole(c.Context(), c.Param("slug"), c.User.ID)
		if err != nil {
			return err
		}
		if !slices.Contains(roles, role) {
			return errNoRole
		}
		return h(c)
	}
}

// workspacePerm is a DRF permission class that requires an active
// workspace membership with one of roles (WorkSpaceAdminPermission,
// WorkspaceViewerPermission, ...).
func (a *API) workspacePerm(roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		role, err := a.workspaceRole(c.Context(), c.Param("slug"), c.User.ID)
		if err != nil {
			return err
		}
		if !slices.Contains(roles, role) {
			return httpx.ErrForbidden
		}
		return h(c)
	}
}

// projectRole is the user's role through an active membership of the
// project (in the workspace with this slug), or 0.
func (a *API) projectRole(ctx context.Context, slug string, project, user uuid.UUID) (int, error) {
	var role int
	err := a.db.QueryRow(ctx, `
		SELECT pm.role FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE w.slug = $1 AND pm.project_id = $2 AND pm.member_id = $3 AND pm.is_active AND pm.deleted_at IS NULL
		ORDER BY pm.role DESC LIMIT 1`, slug, project, user).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return role, err
}

// allowProject is @allow_permission(roles) at project level: a project
// role in roles, or any project membership held by a workspace admin.
func (a *API) allowProject(roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		project, err := c.UUIDParam("project_id")
		if err != nil {
			return err
		}
		ctx := c.Context()
		role, err := a.projectRole(ctx, c.Param("slug"), project, c.User.ID)
		if err != nil {
			return err
		}
		if slices.Contains(roles, role) {
			return h(c)
		}
		if role != 0 {
			wsRole, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
			if err != nil {
				return err
			}
			if wsRole == roleAdmin {
				return h(c)
			}
		}
		return errNoRole
	}
}

// projectBasePerm is the ProjectBasePermission class: reads need an active
// workspace membership, POST a workspace admin or member, and other writes
// a project admin or a workspace admin in the project.
func (a *API) projectBasePerm(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		ctx := c.Context()
		slug := c.Param("slug")
		wsRole, err := a.workspaceRole(ctx, slug, c.User.ID)
		if err != nil {
			return err
		}
		allowed := wsRole != 0
		switch c.R.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		case http.MethodPost:
			allowed = wsRole == roleAdmin || wsRole == roleMember
		default:
			project, err := c.UUIDParam("project_id")
			if err != nil {
				return err
			}
			role, err := a.projectRole(ctx, slug, project, c.User.ID)
			if err != nil {
				return err
			}
			allowed = role == roleAdmin || role != 0 && wsRole == roleAdmin
		}
		if !allowed {
			return httpx.ErrForbidden
		}
		return h(c)
	}
}
