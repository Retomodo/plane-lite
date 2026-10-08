package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 18. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerMiscJobs() {}

func (a *API) registerMiscRoutes(rt *httpx.Router) {
	// plane/app/urls/timezone.py
	// DRF throttles before it looks for a method handler, so the other
	// methods are routed here to count against AuthenticationThrottle.
	rt.HandlePublic("/api/timezones/", httpx.Methods{"GET": a.listTimezones, "POST": a.timezonesNotAllowed,
		"PUT": a.timezonesNotAllowed, "PATCH": a.timezonesNotAllowed, "DELETE": a.timezonesNotAllowed})

	// plane/app/urls/workspace.py: the profile pages. <uuid:user_id> fails
	// URL resolution (404) before any permission class runs.
	rt.Handle(wsPrefix+"user-stats/{user_id}/", httpx.Methods{"GET": userIDParam(a.userProfileStats)})
	rt.Handle(wsPrefix+"user-activity/{user_id}/", httpx.Methods{
		"GET": userIDParam(a.workspacePerm(anyRole, a.userActivity)),
	})
	rt.Handle(wsPrefix+"user-activity/{user_id}/export/", httpx.Methods{
		"POST": userIDParam(a.workspacePerm(adminMember, a.exportUserActivity)),
	})
	rt.Handle(wsPrefix+"user-profile/{user_id}/", httpx.Methods{"GET": userIDParam(a.userProfile)})
	rt.Handle(wsPrefix+"user-issues/{user_id}/", httpx.Methods{
		"GET": userIDParam(a.workspacePerm(anyRole, a.userProfileIssues)),
	})
}

// userIDParam rejects a user_id that isn't a UUID, as the URL converter
// does.
func userIDParam(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		if _, err := c.UUIDParam("user_id"); err != nil {
			return err
		}
		return h(c)
	}
}
