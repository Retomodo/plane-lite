package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 17, plus the issue attachments of section 7. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerAssetJobs() {}

func (a *API) registerAssetRoutes(rt *httpx.Router) {
	_ = rt
}
