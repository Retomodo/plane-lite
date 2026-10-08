package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 11. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerViewJobs() {}

func (a *API) registerViewRoutes(rt *httpx.Router) {
	// plane/app/urls/views.py. PUT on both detail routes is unused by the
	// web app and stays unported; create has no role check (IsAuthenticated
	// only), nor has the workspace view retrieve.
	rt.Handle(projectPrefix+"views/", httpx.Methods{
		"GET":  a.allowProject(anyRole, a.listProjectViews),
		"POST": a.createProjectView,
	})
	rt.Handle(projectPrefix+"views/{pk}/", httpx.Methods{
		"GET":    viewPK(a.allowProject(anyRole, a.getProjectView)),
		"PATCH":  a.allowViewCreator(false, nil, a.updateProjectView),
		"DELETE": a.allowViewCreator(false, adminOnly, a.deleteProjectView),
	})
	rt.Handle(wsPrefix+"views/", httpx.Methods{
		"GET":  a.allowWorkspace(anyRole, a.listWorkspaceViews),
		"POST": a.createWorkspaceView,
	})
	rt.Handle(wsPrefix+"views/{pk}/", httpx.Methods{
		"GET":    a.getWorkspaceView,
		"PATCH":  a.allowViewCreator(true, nil, a.updateWorkspaceView),
		"DELETE": a.allowViewCreator(true, adminOnly, a.deleteWorkspaceView),
	})
	rt.Handle(wsPrefix+"issues/", httpx.Methods{
		"GET": a.allowWorkspace(anyRole, a.listWorkspaceViewIssues),
	})
}
