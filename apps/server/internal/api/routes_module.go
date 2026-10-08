package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 9. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerModuleJobs() {}

func (a *API) registerModuleRoutes(rt *httpx.Router) {
	p := projectPrefix
	// plane/app/urls/module.py. PUT on modules and module links, the module
	// issue list/retrieve/PUT/PATCH, the module link list/retrieve, GET
	// modules/<id>/archive/ and POST/DELETE on archived-modules/ are unused
	// by the web app and stay unported.
	rt.Handle(p+"modules/", httpx.Methods{
		"GET":  a.allowProject(anyRole, a.listModules),
		"POST": a.allowProject(adminMember, a.createModule),
	})
	rt.Handle(p+"modules/{pk}/", httpx.Methods{
		"GET":    a.allowProject(adminMember, a.retrieveModule),
		"PATCH":  a.allowProject(adminMember, a.updateModule),
		"DELETE": a.allowModuleCreator(adminOnly, a.deleteModule),
	})
	rt.Handle(p+"issues/{issue_id}/modules/", httpx.Methods{
		"POST": a.allowProject(adminMember, a.createIssueModules),
	})
	rt.Handle(p+"modules/{module_id}/issues/", httpx.Methods{
		"POST": a.allowProject(adminMember, a.createModuleIssues),
	})
	rt.Handle(p+"modules/{module_id}/issues/{issue_id}/", httpx.Methods{
		"DELETE": a.allowProject(adminMember, a.deleteModuleIssue),
	})
	rt.Handle(p+"modules/{module_id}/module-links/", httpx.Methods{
		"POST": a.projectEntityPerm(a.createModuleLink),
	})
	rt.Handle(p+"modules/{module_id}/module-links/{pk}/", httpx.Methods{
		"PATCH":  a.projectEntityPerm(a.updateModuleLink),
		"DELETE": a.projectEntityPerm(a.deleteModuleLink),
	})
	rt.Handle(p+"modules/{module_id}/user-properties/", httpx.Methods{
		"GET":   a.allowProject(anyRole, a.getModuleUserProperties),
		"PATCH": a.allowProject(anyRole, a.patchModuleUserProperties),
	})
	rt.Handle(p+"modules/{module_id}/archive/", httpx.Methods{
		"POST":   a.projectEntityPerm(a.archiveModule),
		"DELETE": a.projectEntityPerm(a.unarchiveModule),
	})
	rt.Handle(p+"archived-modules/", httpx.Methods{"GET": a.projectEntityPerm(a.listArchivedModules)})
	rt.Handle(p+"archived-modules/{pk}/", httpx.Methods{"GET": a.projectEntityPerm(a.getArchivedModule)})
	rt.Handle(wsPrefix+"modules/", httpx.Methods{"GET": a.workspacePerm(anyRole, a.workspaceModules)})
}
