package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 12. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerPageJobs() {}

func (a *API) registerPageRoutes(rt *httpx.Router) {
	_ = rt
}
