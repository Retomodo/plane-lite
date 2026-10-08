package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 15. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerFavoriteJobs() {}

func (a *API) registerFavoriteRoutes(rt *httpx.Router) {
	// plane/app/urls/workspace.py. The favorite_id variants of GET and POST
	// and the id-less PATCH and DELETE have no caller (Django answers them
	// with a 500), so they stay unrouted.
	rt.Handle(wsPrefix+"user-favorites/", httpx.Methods{
		"GET":  a.allowWorkspace(adminMember, a.listFavorites),
		"POST": a.allowWorkspace(adminMember, a.createFavorite),
	})
	rt.Handle(wsPrefix+"user-favorites/{favorite_id}/", cycleUUIDs(httpx.Methods{
		"PATCH":  a.allowWorkspace(adminMember, a.updateFavorite),
		"DELETE": a.allowWorkspace(adminMember, a.deleteFavorite),
	}, "favorite_id"))
	rt.Handle(wsPrefix+"user-favorites/{favorite_id}/group/", cycleUUIDs(httpx.Methods{
		"GET": a.allowWorkspace(adminMember, a.groupFavorites),
	}, "favorite_id"))
	rt.Handle(wsPrefix+"recent-visits/", httpx.Methods{
		"GET": a.allowWorkspace(anyRole, a.listRecentVisits),
	})

	// plane/app/urls/cycle.py, module.py, views.py, page.py and project.py.
	// The list views are unused.
	rt.Handle(projectPrefix+"user-favorite-cycles/", httpx.Methods{
		"POST": a.allowProject(adminMember, a.createCycleFavorite),
	})
	rt.Handle(projectPrefix+"user-favorite-cycles/{cycle_id}/", cycleUUIDs(httpx.Methods{
		"DELETE": a.allowProject(adminMember, a.deleteCycleFavorite),
	}, "cycle_id"))
	rt.Handle(projectPrefix+"user-favorite-modules/", httpx.Methods{
		"POST": a.projectLitePerm(a.createModuleFavorite),
	})
	rt.Handle(projectPrefix+"user-favorite-modules/{module_id}/", cycleUUIDs(httpx.Methods{
		"DELETE": a.projectLitePerm(a.deleteModuleFavorite),
	}, "module_id"))
	rt.Handle(projectPrefix+"user-favorite-views/", httpx.Methods{
		"POST": a.allowProject(adminMember, a.createViewFavorite),
	})
	rt.Handle(projectPrefix+"user-favorite-views/{view_id}/", cycleUUIDs(httpx.Methods{
		"DELETE": a.allowProject(adminMember, a.deleteViewFavorite),
	}, "view_id"))
	rt.Handle(projectPrefix+"favorite-pages/{page_id}/", cycleUUIDs(httpx.Methods{
		"POST": a.allowProject(adminMember, a.createPageFavorite),
	}, "page_id"))
	rt.Handle(wsPrefix+"user-favorite-projects/", httpx.Methods{
		"POST": a.createProjectFavorite,
	})
	rt.Handle(wsPrefix+"user-favorite-projects/{project_id}/", cycleUUIDs(httpx.Methods{
		"DELETE": a.deleteProjectFavorite,
	}, "project_id"))
}
