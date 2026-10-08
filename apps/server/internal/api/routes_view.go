package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 11. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerViewJobs() {}

func (a *API) registerViewRoutes(rt *httpx.Router) {
	_ = rt
}
