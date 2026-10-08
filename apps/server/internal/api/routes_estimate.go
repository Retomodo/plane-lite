package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 10. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerEstimateJobs() {}

func (a *API) registerEstimateRoutes(rt *httpx.Router) {
	// plane/app/urls/estimate.py
	rt.Handle(projectPrefix+"estimates/", httpx.Methods{
		"GET":  a.projectEntityPerm(a.listEstimates),
		"POST": a.projectEntityPerm(a.createEstimate),
	})
	rt.Handle(projectPrefix+"estimates/{estimate_id}/", httpx.Methods{
		"DELETE": a.projectEntityPerm(a.deleteEstimate),
	})
	rt.Handle(projectPrefix+"estimates/{estimate_id}/estimate-points/", httpx.Methods{
		"POST": a.allowProject(adminMember, a.createEstimatePoint),
	})
	rt.Handle(projectPrefix+"estimates/{estimate_id}/estimate-points/{estimate_point_id}/", httpx.Methods{
		"PATCH": a.allowProject(adminMember, a.updateEstimatePoint),
	})

	// plane/app/urls/workspace.py
	rt.Handle(wsPrefix+"estimates/", httpx.Methods{
		"GET": a.workspacePerm(anyRole, a.workspaceEstimates),
	})
}
