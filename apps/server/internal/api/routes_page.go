package api

import "plane-lite/server/internal/httpx"

// PORTING.md section 12. One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerPageJobs() { a.registerPageTaskJobs() }

func (a *API) registerPageRoutes(rt *httpx.Router) {
	// plane/app/urls/page.py; pages-summary/ is unused (notes/page.md) and
	// favorite-pages/ belongs to section 15.
	page := projectPrefix + "pages/{page_id}/"
	rt.Handle(projectPrefix+"pages/", httpx.Methods{
		"GET":  a.pagePerm(a.listPages),
		"POST": a.pagePerm(a.createPage),
	})
	rt.Handle(page, httpx.Methods{
		"GET":    a.pagePerm(a.getPage),
		"PATCH":  a.pagePerm(a.updatePage),
		"DELETE": a.pagePerm(a.deletePage),
	})
	rt.Handle(page+"archive/", httpx.Methods{
		"POST":   a.pagePerm(a.archivePage),
		"DELETE": a.pagePerm(a.unarchivePage),
	})
	rt.Handle(page+"lock/", httpx.Methods{
		"POST":   a.pagePerm(a.lockPage),
		"DELETE": a.pagePerm(a.unlockPage),
	})
	rt.Handle(page+"access/", httpx.Methods{
		"POST": a.pagePerm(a.pageAccess),
	})
	rt.Handle(page+"description/", httpx.Methods{
		"GET":   a.pagePerm(a.getPageDescription),
		"PATCH": a.pagePerm(a.updatePageDescription),
	})
	rt.Handle(page+"versions/", httpx.Methods{
		"GET": a.pagePerm(a.pageVersions),
	})
	rt.Handle(page+"versions/{pk}/", httpx.Methods{
		"GET": a.pagePerm(a.pageVersions),
	})
	rt.Handle(page+"duplicate/", httpx.Methods{
		"POST": a.pagePerm(a.duplicatePage),
	})
}
