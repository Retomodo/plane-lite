package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// GlobalSearchEndpoint (search/base.py): the command palette's search. Only
// IsAuthenticated: every bucket joins the user's memberships instead. The
// workspaces bucket ignores the slug (any workspace the user has a
// membership row in, active or not); the others need an active project
// membership. Archived cycles, modules and pages, and private pages, are
// all listed.

// searchEntities are the view's MODELS_MAPPER keys, in its order.
var searchEntities = []string{"workspace", "project", "issue", "cycle", "module", "issue_view", "page", "intake"}

type searchWorkspaceRow struct {
	Name string    `json:"name"`
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
}

type searchProjectRow struct {
	Name          string    `json:"name"`
	ID            uuid.UUID `json:"id"`
	Identifier    string    `json:"identifier"`
	WorkspaceSlug string    `json:"workspace__slug"`
}

type searchIssueRow struct {
	Name              string    `json:"name"`
	ID                uuid.UUID `json:"id"`
	SequenceID        int       `json:"sequence_id"`
	ProjectIdentifier string    `json:"project__identifier"`
	ProjectID         uuid.UUID `json:"project_id"`
	WorkspaceSlug     string    `json:"workspace__slug"`
}

// searchProjectEntityRow is a cycle, module or view.
type searchProjectEntityRow struct {
	Name              string    `json:"name"`
	ID                uuid.UUID `json:"id"`
	ProjectID         uuid.UUID `json:"project_id"`
	ProjectIdentifier string    `json:"project__identifier"`
	WorkspaceSlug     string    `json:"workspace__slug"`
}

type searchPageRow struct {
	Name               string      `json:"name"`
	ID                 uuid.UUID   `json:"id"`
	ProjectIDs         []uuid.UUID `json:"project_ids"`
	ProjectIdentifiers []string    `json:"project_identifiers"`
	WorkspaceSlug      string      `json:"workspace__slug"`
}

// globalSearch ports GlobalSearchEndpoint.get. ?entities= picks buckets
// (unknown names dropped); the project-scoped buckets keep to ?project_id=
// only when ?workspace_search is "false" (the default). The intake bucket
// is always empty: intake is cut.
func (a *API) globalSearch(c *httpx.Ctx) error {
	query := c.Query("search")
	workspaceSearch := "false"
	if c.HasQuery("workspace_search") {
		workspaceSearch = c.Query("workspace_search")
	}
	var project *uuid.UUID
	badProject := false
	if raw := c.Query("project_id"); raw != "" && workspaceSearch == "false" {
		id, ok := drf.ParseUUID(raw)
		project, badProject = &id, !ok
	}

	requested := searchEntities
	if c.Query("entities") != "" {
		requested = nil
		for _, e := range strings.Split(c.Query("entities"), ",") {
			if e = drf.PyStrip(e); slices.Contains(searchEntities, e) {
				requested = append(requested, e)
			}
		}
	}
	// filter(project_id=...) raises ValidationError while the querysets are
	// built, before any of them runs.
	if badProject && slices.ContainsFunc(requested, func(e string) bool { return e != "workspace" && e != "project" }) {
		return errFilterDetail
	}

	ctx := c.Context()
	g := globalSearcher{a: a, user: c.User.ID, slug: c.Param("slug"), query: query, project: project}
	results := searchBuckets{}
	for _, e := range requested {
		v, err := g.bucket(ctx, e)
		if err != nil {
			return err
		}
		results.set(e, v)
	}
	return c.JSON(http.StatusOK, map[string]any{"results": results})
}

// globalSearcher runs GlobalSearchEndpoint's filter_* methods.
type globalSearcher struct {
	a       *API
	user    uuid.UUID
	slug    string
	query   string
	project *uuid.UUID // the project filter, when it applies
}

// member is the project__project_projectmember joins' conditions for the
// project aliased p: an active membership and the project not archived.
func (g globalSearcher) member(s *searchSQL) string {
	return `p.archived_at IS NULL AND pm.is_active AND pm.member_id = ` + s.arg(g.user) + ` AND w.slug = ` + s.arg(g.slug)
}

func (g globalSearcher) bucket(ctx context.Context, entity string) (any, error) {
	s := &searchSQL{}
	cond := func(cols ...string) string {
		if g.query == "" {
			return ""
		}
		return " AND " + s.contains(g.query, cols...)
	}
	inProject := func(col string) string {
		if g.project == nil {
			return ""
		}
		return " AND " + col + " = " + s.arg(*g.project)
	}
	switch entity {
	case "workspace":
		// filter_workspaces: no is_active, no deleted_at on the
		// membership, and no slug.
		sql := `SELECT DISTINCT w.name, w.id, w.slug, w.created_at FROM workspaces w
			JOIN workspace_members wm ON wm.workspace_id = w.id
			WHERE w.deleted_at IS NULL` + cond("w.name") + ` AND wm.member_id = ` + s.arg(g.user) + `
			ORDER BY w.created_at DESC`
		return searchRows[searchWorkspaceRowAt](ctx, g.a, sql, s.args)
	case "project":
		sql := `SELECT DISTINCT p.name, p.id, p.identifier, w.slug, p.created_at FROM projects p
			JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = p.workspace_id
			WHERE p.deleted_at IS NULL` + cond("p.name", "p.identifier") + ` AND ` + g.member(s) + `
			ORDER BY p.created_at DESC`
		return searchRows[searchProjectRowAt](ctx, g.a, sql, s.args)
	case "issue":
		where := ""
		if g.query != "" {
			where = " AND " + s.issueQuery(g.query, true)
		}
		sql := `SELECT DISTINCT i.name, i.id, i.sequence_id, p.identifier, i.project_id, w.slug, i.created_at
			` + searchIssueFrom + ` WHERE ` + searchIssueLive + where + ` AND ` + g.member(s) + inProject("i.project_id") + `
			ORDER BY i.created_at DESC LIMIT 100`
		return searchRows[searchIssueRowAt](ctx, g.a, sql, s.args)
	case "cycle", "module", "issue_view":
		table := map[string]string{"cycle": "cycles", "module": "modules", "issue_view": "issue_views"}[entity]
		sql := `SELECT DISTINCT x.name, x.id, x.project_id, p.identifier, w.slug, x.created_at FROM ` + table + ` x
			JOIN projects p ON p.id = x.project_id JOIN project_members pm ON pm.project_id = p.id
			JOIN workspaces w ON w.id = x.workspace_id
			WHERE x.deleted_at IS NULL` + cond("x.name") + ` AND ` + g.member(s) + inProject("x.project_id") + `
			ORDER BY x.created_at DESC`
		return searchRows[searchProjectEntityRowAt](ctx, g.a, sql, s.args)
	case "page":
		// The project arrays aggregate over the filter's joins, so they
		// only name projects the user is an active member of. Their
		// filter=~Q(projects__id=True) compares with uuid int 1: a no-op.
		// The project_pages join ignores deleted_at; the project filter's
		// subquery doesn't.
		scope := ""
		if g.project != nil {
			p := s.arg(*g.project)
			scope = ` AND (SELECT pp2.project_id FROM project_pages pp2
				WHERE pp2.deleted_at IS NULL AND pp2.page_id = pg.id AND pp2.project_id = ` + p + `
				ORDER BY pp2.created_at DESC LIMIT 1) = ` + p
		}
		sql := `SELECT DISTINCT pg.name, pg.id,
				COALESCE(array_agg(DISTINCT pp.project_id) FILTER (WHERE NOT (pp.project_id = '00000000-0000-0000-0000-000000000001')), '{}'::uuid[]),
				COALESCE(array_agg(DISTINCT p.identifier) FILTER (WHERE NOT (pp.project_id = '00000000-0000-0000-0000-000000000001')), '{}'::varchar[]),
				w.slug, pg.created_at
			FROM pages pg JOIN project_pages pp ON pp.page_id = pg.id JOIN projects p ON p.id = pp.project_id
			JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = pg.workspace_id
			WHERE pg.deleted_at IS NULL` + cond("pg.name") + ` AND ` + g.member(s) + scope + `
			GROUP BY pg.id, w.slug ORDER BY pg.created_at DESC`
		return searchRows[searchPageRowAt](ctx, g.a, sql, s.args)
	}
	// intake: cut, so there are no intake issues to find.
	return []searchIssueRow{}, nil
}

// The *At types scan a row with its trailing created_at (selected for
// DISTINCT ... ORDER BY), which the JSON leaves out.

type searchWorkspaceRowAt struct {
	searchWorkspaceRow
	CreatedAt searchIgnored `json:"-"`
}

type searchProjectRowAt struct {
	searchProjectRow
	CreatedAt searchIgnored `json:"-"`
}

type searchIssueRowAt struct {
	searchIssueRow
	CreatedAt searchIgnored `json:"-"`
}

type searchProjectEntityRowAt struct {
	searchProjectEntityRow
	CreatedAt searchIgnored `json:"-"`
}

type searchPageRowAt struct {
	searchPageRow
	CreatedAt searchIgnored `json:"-"`
}

// searchIgnored scans and drops a column.
type searchIgnored struct{}

func (*searchIgnored) Scan(any) error { return nil }

// searchIssueFrom and searchIssueLive are Issue.issue_objects joined to the
// user's project memberships, as the search views' querysets compile.
const (
	searchIssueFrom = `FROM issues i LEFT JOIN states s ON s.id = i.state_id JOIN projects p ON p.id = i.project_id
		JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = i.workspace_id`
	searchIssueLive = `i.deleted_at IS NULL AND NOT (s."group" = 'triage' AND s."group" IS NOT NULL)
		AND i.archived_at IS NULL AND p.archived_at IS NULL AND NOT i.is_draft`
)
