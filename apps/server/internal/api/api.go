// Package api implements Plane's HTTP endpoints. Each handler documents the
// Django view it ports; behaviour (status codes, bodies, side effects) must
// match the contract goldens recorded from that view.
package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"plane-lite/server/internal/auth"
	"plane-lite/server/internal/config"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
	"plane-lite/server/internal/mail"
	"plane-lite/server/internal/throttle"
)

type API struct {
	cfg      *config.Config
	db       *pgxpool.Pool
	rdb      *redis.Client
	log      *slog.Logger
	sessions *auth.Sessions
	csrf     *auth.CSRF
	limiter  *throttle.Limiter
	jobs     *jobs.Runner
	mailer   *mail.Mailer

	anonRate throttle.Rate // DRF DEFAULT_THROTTLE_RATES["anon"]
	authRate throttle.Rate // AuthenticationThrottle (AUTHENTICATION_RATE_LIMIT)
}

type Deps struct {
	Config   *config.Config
	DB       *pgxpool.Pool
	Redis    *redis.Client
	Log      *slog.Logger
	Sessions *auth.Sessions
	Jobs     *jobs.Runner
	Mailer   *mail.Mailer
}

func New(d Deps) *API {
	authRate, err := throttle.ParseRate(d.Config.AuthenticationRateLimit)
	if err != nil {
		authRate = throttle.MustRate("10/minute")
	}
	a := &API{
		cfg:      d.Config,
		db:       d.DB,
		rdb:      d.Redis,
		log:      d.Log,
		sessions: d.Sessions,
		csrf:     auth.NewCSRF(d.Config),
		limiter:  throttle.New(d.Redis, d.Config.RedisKeyPrefix),
		anonRate: throttle.MustRate("30/minute"),
		authRate: authRate,
		jobs:     d.Jobs,
		mailer:   d.Mailer,
	}
	a.registerEmailJobs()
	a.registerPasswordJobs()
	a.registerUserJobs()
	a.registerWorkspaceJobs()
	a.registerProjectJobs()
	a.registerIssueJobs()
	a.registerIssueDiscussionJobs()
	a.registerIssueStructureJobs()
	a.registerCycleJobs()
	a.registerModuleJobs()
	a.registerEstimateJobs()
	a.registerViewJobs()
	a.registerPageJobs()
	a.registerHomeJobs()
	a.registerNotificationJobs()
	a.registerFavoriteJobs()
	a.registerSearchJobs()
	a.registerAssetJobs()
	a.registerMiscJobs()
	return a
}

// wsPrefix and projectPrefix are the W/ and P/ URL prefixes of PORTING.md.
const (
	wsPrefix      = "/api/workspaces/{slug}/"
	projectPrefix = wsPrefix + "projects/{project_id}/"
)

// Register mounts every ported endpoint.
func (a *API) Register(rt *httpx.Router) {
	// plane/license/urls.py
	rt.HandlePublic("/api/instances/", httpx.Methods{"GET": a.anon(a.getInstance)})

	// plane/authentication/urls.py
	rt.HandlePublic("/auth/get-csrf-token/", httpx.Methods{"GET": a.anon(a.getCSRFToken)})
	rt.HandlePublic("/auth/email-check/", httpx.Methods{"POST": a.emailCheck})
	rt.HandlePublic("/auth/sign-up/", httpx.Methods{"POST": a.signUp})
	rt.HandlePublic("/auth/sign-in/", httpx.Methods{"POST": a.signIn})
	rt.HandlePublic("/auth/sign-out/", httpx.Methods{"POST": a.signOut})
	rt.HandlePublic("/auth/magic-generate/", httpx.Methods{"POST": a.magicGenerate})
	rt.HandlePublic("/auth/magic-sign-in/", httpx.Methods{"POST": a.magicSignIn})
	rt.HandlePublic("/auth/magic-sign-up/", httpx.Methods{"POST": a.magicSignUp})
	rt.HandlePublic("/auth/forgot-password/", httpx.Methods{"POST": a.forgotPassword})
	rt.HandlePublic("/auth/reset-password/{uidb64}/{token}/", httpx.Methods{"POST": a.resetPassword})
	rt.Handle("/auth/change-password/", httpx.Methods{"POST": a.changePassword})
	rt.Handle("/auth/set-password/", httpx.Methods{"POST": a.setPassword})

	// plane/app/urls/user.py
	rt.Handle("/api/users/me/", httpx.Methods{"GET": a.getMe, "PATCH": a.patchMe, "DELETE": a.deactivateMe})
	rt.HandlePublic("/api/users/session/", httpx.Methods{"GET": a.anon(a.userSession)})
	rt.Handle("/api/users/me/settings/", httpx.Methods{"GET": a.userSettings})
	rt.Handle("/api/users/me/email/generate-code/", httpx.Methods{"POST": a.emailGenerateCode})
	rt.Handle("/api/users/me/email/", httpx.Methods{"PATCH": a.updateEmail})
	rt.Handle("/api/users/me/profile/", httpx.Methods{"GET": a.getProfile, "PATCH": a.patchProfile})
	rt.Handle("/api/users/me/accounts/", httpx.Methods{"GET": a.listAccounts, "DELETE": a.accountsNoPK})
	rt.Handle("/api/users/me/accounts/{pk}/", httpx.Methods{"GET": a.account, "DELETE": a.account})
	rt.Handle("/api/users/me/instance-admin/", httpx.Methods{"GET": a.instanceAdmin})
	rt.Handle("/api/users/me/onboard/", httpx.Methods{"PATCH": a.setProfileFlag("is_onboarded")})
	rt.Handle("/api/users/me/tour-completed/", httpx.Methods{"PATCH": a.setProfileFlag("is_tour_completed")})
	rt.Handle("/api/users/me/workspaces/", httpx.Methods{"GET": a.myWorkspaces})

	// plane/app/urls/workspace.py
	const ws = "/api/workspaces/{slug}/"
	rt.Handle("/api/workspace-slug-check/", httpx.Methods{"GET": a.workspaceSlugCheck})
	rt.Handle("/api/workspaces/", httpx.Methods{"GET": a.listWorkspaces, "POST": a.createWorkspace})
	rt.Handle(ws, httpx.Methods{
		"GET":    a.getWorkspace,
		"PUT":    a.workspacePerm(adminMember, a.updateWorkspace(false)),
		"PATCH":  a.workspacePerm(adminMember, a.allowWorkspace(adminOnly, a.updateWorkspace(true))),
		"DELETE": a.workspacePerm(adminOnly, a.allowWorkspace(adminOnly, a.deleteWorkspace)),
	})
	rt.Handle(ws+"invitations/", httpx.Methods{
		"GET":  a.workspacePerm(adminMember, a.listInvites),
		"POST": a.workspacePerm(adminMember, a.createInvites),
	})
	rt.Handle(ws+"invitations/{pk}/", httpx.Methods{
		"GET":    a.workspacePerm(adminMember, a.getInvite),
		"PATCH":  a.workspacePerm(adminMember, a.patchInvite),
		"DELETE": a.workspacePerm(adminMember, a.deleteInvite),
	})
	rt.Handle("/api/users/me/workspaces/invitations/", httpx.Methods{"GET": a.myInvites, "POST": a.acceptMyInvites})
	rt.HandlePublic(ws+"invitations/{pk}/join/", httpx.Methods{"GET": a.anon(a.joinInviteInfo), "POST": a.anon(a.joinInvite)})
	rt.Handle(ws+"members/", httpx.Methods{"GET": a.allowWorkspace(anyRole, a.listMembers)})
	rt.Handle(ws+"members/{pk}/", httpx.Methods{
		"GET":    a.allowWorkspace(anyRole, a.getMember),
		"PATCH":  a.allowWorkspace(adminOnly, a.patchMember),
		"DELETE": a.allowWorkspace(adminOnly, a.deleteMember),
	})
	rt.Handle(ws+"members/leave/", httpx.Methods{"POST": a.allowWorkspace(anyRole, a.leaveWorkspace)})
	rt.Handle(ws+"workspace-members/me/", httpx.Methods{"GET": a.workspaceMemberMe})
	rt.Handle(ws+"workspace-views/", httpx.Methods{"POST": a.saveWorkspaceViewProps})
	rt.Handle(ws+"user-properties/", httpx.Methods{
		"GET":   a.workspacePerm(anyRole, a.getWorkspaceUserProperties),
		"PATCH": a.workspacePerm(anyRole, a.patchWorkspaceUserProperties),
	})
	rt.Handle(ws+"sidebar-preferences/", httpx.Methods{
		"GET":   a.allowWorkspace(anyRole, a.getSidebarPreferences),
		"PATCH": a.allowWorkspace(anyRole, a.patchSidebarPreferences),
	})
	rt.Handle(ws+"project-members/", httpx.Methods{"GET": a.workspacePerm(anyRole, a.workspaceProjectMembers)})

	// plane/app/urls/project.py
	const p = ws + "projects/{project_id}/"
	rt.Handle(ws+"projects/", httpx.Methods{
		"GET":  a.allowWorkspace(anyRole, a.listProjects),
		"POST": a.allowWorkspace(adminMember, a.createProject),
	})
	rt.Handle(ws+"projects/details/", httpx.Methods{"GET": a.allowWorkspace(anyRole, a.listProjectDetails)})
	rt.Handle(ws+"projects/{pk}/", httpx.Methods{
		"GET":    a.allowWorkspace(anyRole, a.getProject),
		"PATCH":  a.updateProject,
		"DELETE": a.deleteProject,
	})
	rt.Handle(ws+"project-identifiers/", httpx.Methods{"GET": a.allowWorkspace(adminMember, a.projectIdentifiers)})
	rt.Handle("/api/users/me/workspaces/{slug}/projects/invitations/", httpx.Methods{
		"POST": a.allowWorkspace(adminMember, a.joinProjects),
	})
	rt.Handle("/api/users/me/workspaces/{slug}/project-roles/", httpx.Methods{"GET": a.workspacePerm(anyRole, a.projectRoles)})
	rt.Handle(p+"members/", httpx.Methods{
		"GET":  a.allowProject(anyRole, a.listProjectMembers),
		"POST": a.allowProject(adminOnly, a.createProjectMembers),
	})
	rt.Handle(p+"members/{pk}/", httpx.Methods{
		"GET":    a.allowProject(anyRole, a.getProjectMember),
		"PATCH":  a.allowProject(anyRole, a.patchProjectMember),
		"DELETE": a.allowProject(adminOnly, a.deleteProjectMember),
	})
	rt.Handle(p+"members/leave/", httpx.Methods{"POST": a.allowProject(anyRole, a.leaveProject)})
	rt.Handle(p+"project-members/me/", httpx.Methods{"GET": a.projectMemberMe})
	rt.Handle(p+"archive/", httpx.Methods{
		"POST":   a.allowProject(adminMember, a.archiveProject),
		"DELETE": a.allowProject(adminMember, a.unarchiveProject),
	})

	// plane/app/urls/state.py
	rt.Handle(p+"states/", httpx.Methods{
		"GET":  a.allowProject(anyRole, a.listStates),
		"POST": a.allowProject(adminOnly, a.createState),
	})
	rt.Handle(p+"states/{pk}/", httpx.Methods{
		"GET":    a.getState,
		"PATCH":  a.allowProject(anyRole, a.updateState),
		"DELETE": a.allowProject(adminOnly, a.deleteState),
	})
	rt.Handle(p+"states/{pk}/mark-default/", httpx.Methods{"POST": a.allowProject(adminOnly, a.markDefaultState)})
	rt.Handle(p+"intake-state/", httpx.Methods{"GET": a.allowProject(anyRole, a.intakeState)})
	rt.Handle(ws+"states/", httpx.Methods{"GET": a.workspacePerm(anyRole, a.workspaceStates)})

	// plane/app/urls/issue.py
	rt.Handle(p+"issue-labels/", httpx.Methods{
		"GET":  a.projectBasePerm(a.listLabels),
		"POST": a.projectBasePerm(a.allowProject(adminOnly, a.createLabel)),
	})
	rt.Handle(p+"issue-labels/{pk}/", httpx.Methods{
		"PATCH":  a.projectBasePerm(a.allowProject(adminOnly, a.updateLabel)),
		"DELETE": a.projectBasePerm(a.allowProject(adminOnly, a.deleteLabel)),
	})
	rt.Handle(ws+"labels/", httpx.Methods{"GET": a.workspacePerm(anyRole, a.workspaceLabels)})
	rt.Handle(p+"issues/", httpx.Methods{
		"GET":  a.allowProject(anyRole, a.listIssues),
		"POST": a.allowProject(adminMember, a.createIssue),
	})
	rt.Handle(p+"issues/list/", httpx.Methods{"GET": a.allowProject(anyRole, a.listIssuesByID)})
	rt.Handle(p+"archived-issues/", httpx.Methods{"GET": a.allowProject(adminMember, a.listArchivedIssues)})
	rt.Handle(p+"issues/{pk}/", httpx.Methods{
		"GET":    a.allowIssue(anyRole, a.getIssue),
		"PATCH":  a.allowIssue(adminMember, a.updateIssue),
		"DELETE": a.allowIssue(adminOnly, a.deleteIssue),
	})
	rt.Handle(p+"issues/{issue_id}/meta/", httpx.Methods{"GET": a.allowProject(anyRole, a.issueMeta)})
	rt.Handle(p+"issues/{pk}/archive/", httpx.Methods{
		"GET":    a.allowProject(adminMember, a.getArchivedIssue),
		"POST":   a.allowProject(adminMember, a.archiveIssue),
		"DELETE": a.allowProject(adminMember, a.unarchiveIssue),
	})
	rt.Handle(p+"bulk-archive-issues/", httpx.Methods{
		"POST": a.projectEntityPerm(a.allowProject(adminMember, a.bulkArchiveIssues)),
	})
	rt.Handle(p+"bulk-delete-issues/", httpx.Methods{"DELETE": a.allowProject(adminOnly, a.bulkDeleteIssues)})
	rt.Handle(p+"issue-dates/", httpx.Methods{"POST": a.allowProject(adminMember, a.issueDates)})
	rt.Handle(ws+"work-items/{ident}/", httpx.Methods{"GET": a.getIssueByIdentifier})
	rt.Handle(p+"user-properties/", httpx.Methods{
		"GET":   a.allowProject(anyRole, a.getProjectUserProperties),
		"PATCH": a.allowProject(anyRole, a.patchProjectUserProperties),
	})

	// Batches 9 onward, one file per area (routes_*.go).
	a.registerIssueDiscussionRoutes(rt)
	a.registerIssueStructureRoutes(rt)
	a.registerCycleRoutes(rt)
	a.registerModuleRoutes(rt)
	a.registerEstimateRoutes(rt)
	a.registerViewRoutes(rt)
	a.registerPageRoutes(rt)
	a.registerHomeRoutes(rt)
	a.registerNotificationRoutes(rt)
	a.registerFavoriteRoutes(rt)
	a.registerSearchRoutes(rt)
	a.registerAssetRoutes(rt)
	a.registerMiscRoutes(rt)
}

// anon applies DRF's default AnonRateThrottle to an AllowAny view.
func (a *API) anon(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		if c.User == nil {
			if err := a.throttle(c, "anon", a.anonRate); err != nil {
				return err
			}
		}
		return h(c)
	}
}

// throttle returns Plane's 429 body when the client is over rate.
func (a *API) throttle(c *httpx.Ctx, scope string, rate throttle.Rate) error {
	ok, wait, err := a.limiter.Allow(c.Context(), scope, throttle.Ident(c.R), rate)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return a.rateLimited(c, wait)
}

// rateLimited is Plane's throttle failure response.
func (a *API) rateLimited(c *httpx.Ctx, wait time.Duration) error {
	if wait > 0 {
		c.W.Header().Set("Retry-After", throttle.RetryAfter(wait))
	}
	return httpx.Body(http.StatusTooManyRequests, authErr("RATE_LIMIT_EXCEEDED").body())
}
