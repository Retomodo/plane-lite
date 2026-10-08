package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 9. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerModuleJobs() {}

func (a *API) registerModuleRoutes(rt *httpx.Router) {
	_ = rt
}
