package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 16. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerSearchJobs() {}

func (a *API) registerSearchRoutes(rt *httpx.Router) {
	// plane/app/urls/search.py. The global and issue searches are only
	// IsAuthenticated; their queries join the user's memberships.
	rt.Handle(wsPrefix+"search/", httpx.Methods{"GET": a.globalSearch})
	rt.Handle(projectPrefix+"search-issues/", httpx.Methods{"GET": a.searchIssues})
	rt.Handle(wsPrefix+"entity-search/", httpx.Methods{"GET": a.workspacePerm(anyRole, a.searchEntity)})
}
