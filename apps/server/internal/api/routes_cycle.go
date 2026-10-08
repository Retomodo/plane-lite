package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 8. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerCycleJobs() {}

func (a *API) registerCycleRoutes(rt *httpx.Router) {
	_ = rt
}
