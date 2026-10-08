package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 15. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerFavoriteJobs() {}

func (a *API) registerFavoriteRoutes(rt *httpx.Router) {
	_ = rt
}
