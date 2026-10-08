package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 16. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerSearchJobs() {}

func (a *API) registerSearchRoutes(rt *httpx.Router) {
	_ = rt
}
