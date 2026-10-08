package api

import (
	"context"

	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/jobs"
)

// PORTING.md section 7 (sub-issues, links, relations, description versions). One porting agent owns this file and the
// handlers it routes to; see contract/PLAYBOOK.md.

func (a *API) registerIssueStructureJobs() {
	jobs.Register(a.jobs, func(ctx context.Context, j crawlLinkJob) error {
		if err := a.crawlLinkTitle(ctx, j); err != nil {
			// A failed save escapes the task; Celery logs it.
			a.log.Error("crawl_work_item_link_title", "err", err)
		}
		return nil
	})
}

func (a *API) registerIssueStructureRoutes(rt *httpx.Router) {
	p := projectPrefix
	// plane/app/urls/issue.py. The issue-links retrieve and PUT routes and
	// issues/<id>/versions/ are unused by the web app and stay unported.
	rt.Handle(p+"issues/{issue_id}/sub-issues/", httpx.Methods{
		"GET":  a.projectEntityPerm(a.listSubIssues),
		"POST": a.projectEntityPerm(a.addSubIssues),
	})
	rt.Handle(p+"issues/{issue_id}/issue-links/", httpx.Methods{
		"GET":  a.projectEntityPerm(a.listIssueLinks),
		"POST": a.projectEntityPerm(a.createIssueLink),
	})
	rt.Handle(p+"issues/{issue_id}/issue-links/{pk}/", httpx.Methods{
		"PATCH":  a.projectEntityPerm(a.updateIssueLink),
		"DELETE": a.projectEntityPerm(a.deleteIssueLink),
	})
	rt.Handle(p+"issues/{issue_id}/issue-relation/", httpx.Methods{
		"GET":  a.projectEntityPerm(a.listIssueRelations),
		"POST": a.projectEntityPerm(a.createIssueRelations),
	})
	rt.Handle(p+"issues/{issue_id}/remove-relation/", httpx.Methods{
		"POST": a.projectEntityPerm(a.removeIssueRelation),
	})
	rt.Handle(p+"work-items/{work_item_id}/description-versions/", httpx.Methods{
		"GET": a.allowProject(anyRole, a.descriptionVersions),
	})
	rt.Handle(p+"work-items/{work_item_id}/description-versions/{pk}/", httpx.Methods{
		"GET": a.allowProject(anyRole, a.descriptionVersions),
	})
}
