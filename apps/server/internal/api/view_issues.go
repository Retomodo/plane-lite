package api

import (
	"plane-lite/server/internal/httpx"
)

// viewIssueFrom is the issue queryset's joins plus the viewer's project
// membership, which WorkspaceViewIssuesViewSet's permission filter joins
// once (one filter() call).
const viewIssueFrom = ` FROM issues i LEFT JOIN states s ON s.id = i.state_id JOIN projects p ON p.id = i.project_id
	JOIN workspaces w ON w.id = i.workspace_id JOIN project_members pm ON pm.project_id = p.id`

// viewIssueIDArrays are ViewIssueListSerializer's id lists, read from the
// prefetched relations: every live row, newest first (their Meta
// ordering), with no filter on the related label, user or module.
var viewIssueIDArrays = map[string]string{
	"assignee_ids": `COALESCE((SELECT array_agg(ia.assignee_id ORDER BY ia.created_at DESC) FROM issue_assignees ia
		WHERE ia.issue_id = i.id AND ia.deleted_at IS NULL), '{}')`,
	"label_ids": `COALESCE((SELECT array_agg(il.label_id ORDER BY il.created_at DESC) FROM issue_labels il
		WHERE il.issue_id = i.id AND il.deleted_at IS NULL), '{}')`,
	"module_ids": `COALESCE((SELECT array_agg(mi.module_id ORDER BY mi.created_at DESC) FROM module_issues mi
		WHERE mi.issue_id = i.id AND mi.deleted_at IS NULL), '{}')`,
}

// listWorkspaceViewIssues ports WorkspaceViewIssuesViewSet.list
// (W/issues/): the workspace's issues in the projects where the user is an
// active member (a guest of a project that doesn't share everything sees
// only their own), through the rich and legacy filters and the issue
// ordering, always as one flat page: the view passes the paginator no
// grouping. Nothing makes the query distinct, so joins to several matching
// relation rows repeat an issue (and count it twice).
func (a *API) listWorkspaceViewIssues(c *httpx.Ctx) error {
	l := &issueList{a: a, c: c, slug: c.Param("slug"), from: viewIssueFrom, plainCounts: true, workspace: true,
		idArraySQL: viewIssueIDArrays, q: &issueQuery{tz: c.Loc().String()}}
	q := l.q
	q.filter(issueBaseWhere)
	q.filter("w.slug = " + q.arg(l.slug))
	if err := l.applyFilters(false); err != nil {
		return err
	}
	me := q.arg(c.User.ID)
	q.filter(`((p.guest_view_all_features AND pm.role = 5)
		OR (i.created_by_id = ` + me + ` AND NOT p.guest_view_all_features AND pm.role = 5) OR pm.role > 5)
		AND pm.is_active AND pm.member_id = ` + me)
	l.filtered = q.clone()
	orderBy := c.Query("order_by")
	if !c.HasQuery("order_by") {
		orderBy = "-created_at"
	}
	l.order = q.orderIssues(orderBy)
	page, err := parseOffsetPage(c)
	if err != nil {
		return err
	}
	return l.paginateFlat(c.Context(), page)
}
