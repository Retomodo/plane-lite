package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 13. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerHomeJobs() {}

func (a *API) registerHomeRoutes(rt *httpx.Router) {
	// plane/app/urls/workspace.py. The GET-by-id routes of quick links and
	// stickies, and the key-less PATCH and keyed GET of home preferences,
	// are unused by the web app and stay unported.
	rt.Handle(wsPrefix+"quick-links/", httpx.Methods{
		"GET":  a.allowWorkspace(anyRole, a.listQuickLinks),
		"POST": a.allowWorkspace(anyRole, a.createQuickLink),
	})
	rt.Handle(wsPrefix+"quick-links/{pk}/", httpx.Methods{
		"PATCH":  a.allowWorkspace(anyRole, a.updateQuickLink),
		"DELETE": a.allowWorkspace(anyRole, a.deleteQuickLink),
	})
	rt.Handle(wsPrefix+"home-preferences/", httpx.Methods{
		"GET": a.allowWorkspace(anyRole, a.getHomePreferences),
	})
	rt.Handle(wsPrefix+"home-preferences/{key}/", httpx.Methods{
		"PATCH": a.allowWorkspace(anyRole, a.patchHomePreference),
	})
	rt.Handle(wsPrefix+"stickies/", httpx.Methods{
		"GET":  a.allowWorkspace(anyRole, a.listStickies),
		"POST": a.allowWorkspace(anyRole, a.createSticky),
	})
	rt.Handle(wsPrefix+"stickies/{pk}/", httpx.Methods{
		"PATCH":  a.allowStickyCreator(a.updateSticky),
		"DELETE": a.allowStickyCreator(a.deleteSticky),
	})
}
