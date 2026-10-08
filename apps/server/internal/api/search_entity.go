package api

import (
	"context"
	"math/big"
	"net/http"
	"strings"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// SearchEndpoint (search/base.py): the editor's mention and link picker.
// WorkspaceUserPermission. With ?project_id= each type keeps to that
// project, except "project"; without it user mentions are workspace
// members and pages only global ones. Unlike the global search, nothing
// here hides archived projects' cycles and modules, and the project type
// lists every public project plus any the user has a membership row in,
// active or not.

type searchMentionRow struct {
	AvatarURL   *string   `json:"member__avatar_url"`
	DisplayName string    `json:"member__display_name"`
	MemberID    uuid.UUID `json:"member__id"`
}

type searchMentionRowAt struct {
	searchMentionRow
	CreatedAt searchIgnored `json:"-"`
}

type searchEntityProjectRow struct {
	Name          string         `json:"name"`
	ID            uuid.UUID      `json:"id"`
	Identifier    string         `json:"identifier"`
	LogoProps     jsontext.Value `json:"logo_props"`
	WorkspaceSlug string         `json:"workspace__slug"`
	CreatedAt     searchIgnored  `json:"-"`
}

type searchEntityIssueRow struct {
	Name              string        `json:"name"`
	ID                uuid.UUID     `json:"id"`
	SequenceID        int           `json:"sequence_id"`
	ProjectIdentifier string        `json:"project__identifier"`
	ProjectID         uuid.UUID     `json:"project_id"`
	Priority          *string       `json:"priority"`
	StateID           *uuid.UUID    `json:"state_id"`
	TypeID            *uuid.UUID    `json:"type_id"`
	CreatedAt         searchIgnored `json:"-"`
}

// searchEntityStatusRow is a cycle (status computed from its dates) or a
// module (its status column).
type searchEntityStatusRow struct {
	Name              string        `json:"name"`
	ID                uuid.UUID     `json:"id"`
	ProjectID         uuid.UUID     `json:"project_id"`
	ProjectIdentifier string        `json:"project__identifier"`
	Status            *string       `json:"status"`
	WorkspaceSlug     string        `json:"workspace__slug"`
	CreatedAt         searchIgnored `json:"-"`
}

type searchEntityPageRow struct {
	Name          string         `json:"name"`
	ID            uuid.UUID      `json:"id"`
	LogoProps     jsontext.Value `json:"logo_props"`
	ProjectsID    uuid.UUID      `json:"projects__id"`
	WorkspaceSlug string         `json:"workspace__slug"`
	CreatedAt     searchIgnored  `json:"-"`
}

// searchAvatarURL is the views' member__avatar_url annotation for users u.
const searchAvatarURL = `CASE WHEN u.avatar_asset_id IS NOT NULL THEN '/api/assets/v2/static/' || u.avatar_asset_id::varchar || '/'
	WHEN u.avatar_asset_id IS NULL THEN u.avatar ELSE NULL END`

// searchCycleStatus is the cycle status annotation, against the time of the
// query.
const searchCycleStatus = `CASE WHEN (c.start_date <= now() AND c.end_date >= now()) THEN 'CURRENT'
	WHEN c.start_date > now() THEN 'UPCOMING' WHEN c.end_date < now() THEN 'COMPLETED'
	WHEN (c.start_date IS NULL AND c.end_date IS NULL) THEN 'DRAFT' ELSE 'DRAFT' END`

// searchEntity ports SearchEndpoint.get. ?query_type= is a comma list
// (default user_mention; unknown types skipped), ?count= the per-type limit
// (default 5, parsed with int(): a bad or negative count is a 500).
func (a *API) searchEntity(c *httpx.Ctx) error {
	types := "user_mention"
	if c.HasQuery("query_type") {
		types = c.Query("query_type")
	}
	countRaw := "5"
	if c.HasQuery("count") {
		countRaw = c.Query("count")
	}
	count, ok := drf.PyIntString(countRaw)
	if !ok {
		return errViewCrash // int() raises ValueError
	}
	e := entitySearcher{a: a, user: c.User.ID, slug: c.Param("slug"), query: c.Query("query"), count: count,
		projectRaw: c.Query("project_id")}
	if e.projectRaw != "" {
		e.project, e.projectOK = drf.ParseUUID(e.projectRaw)
	}
	ctx := c.Context()
	out := searchBuckets{}
	for _, t := range strings.Split(types, ",") {
		t = drf.PyStrip(t)
		v, err := e.bucket(ctx, t)
		if err != nil {
			return err
		}
		if v != nil {
			out.set(t, v)
		}
	}
	return c.JSON(http.StatusOK, out)
}

type entitySearcher struct {
	a          *API
	user       uuid.UUID
	slug       string
	query      string
	count      *big.Int
	projectRaw string
	project    uuid.UUID
	projectOK  bool
}

func (e entitySearcher) scoped() bool { return e.projectRaw != "" }

// limit is the queryset slice [:count]: negative counts raise, zero
// answers [] without a query, and a count past bigint fails in Postgres.
func (e entitySearcher) limit() (string, bool, error) {
	switch {
	case e.count.Sign() < 0:
		return "", false, errViewCrash
	case e.count.Sign() == 0:
		return "", false, nil
	case !e.count.IsInt64():
		return "", false, errViewCrash
	}
	return " LIMIT " + e.count.String(), true, nil
}

// bucket answers one query type, or nil for an unknown one.
func (e entitySearcher) bucket(ctx context.Context, t string) (any, error) {
	s := &searchSQL{}
	cond := func(cols ...string) string {
		if e.query == "" {
			return ""
		}
		return " AND " + s.contains(e.query, cols...)
	}
	// inProject is the project_id filter, which raises ValidationError on
	// a malformed id when the queryset is built.
	inProject := func(col string) (string, error) {
		if !e.scoped() {
			return "", nil
		}
		if !e.projectOK {
			return "", errFilterDetail
		}
		return " AND " + col + " = " + s.arg(e.project), nil
	}
	member := func() string {
		return ` AND pm.is_active AND pm.member_id = ` + s.arg(e.user)
	}
	slug := func() string { return ` AND w.slug = ` + s.arg(e.slug) }
	var sql string
	switch t {
	case "user_mention":
		fields := cond("u.first_name", "u.last_name", "u.display_name")
		if e.scoped() {
			project, err := inProject("pm.project_id")
			if err != nil {
				return nil, err
			}
			sql = `SELECT DISTINCT ` + searchAvatarURL + `, u.display_name, pm.member_id, pm.created_at
				FROM project_members pm LEFT JOIN users u ON u.id = pm.member_id JOIN workspaces w ON w.id = pm.workspace_id
				WHERE pm.deleted_at IS NULL` + fields + ` AND pm.is_active AND NOT u.is_bot` + project + slug() + `
				ORDER BY pm.created_at DESC`
		} else {
			// No DISTINCT here; created_at is selected only to share the
			// row type.
			sql = `SELECT ` + searchAvatarURL + `, u.display_name, wm.member_id, wm.created_at
				FROM workspace_members wm JOIN users u ON u.id = wm.member_id JOIN workspaces w ON w.id = wm.workspace_id
				WHERE wm.deleted_at IS NULL` + fields + ` AND wm.is_active AND NOT u.is_bot` + slug() + `
				ORDER BY wm.created_at DESC`
		}
		return searchLimited[searchMentionRowAt](ctx, e, sql, s.args)
	case "project":
		// The OR across the membership join makes it a LEFT JOIN, with no
		// is_active or deleted_at check, and archived projects stay in.
		sql = `SELECT DISTINCT p.name, p.id, p.identifier, p.logo_props::text, w.slug, p.created_at
			FROM projects p LEFT JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = p.workspace_id
			WHERE p.deleted_at IS NULL` + cond("p.name", "p.identifier") + ` AND (pm.member_id = ` + s.arg(e.user) + `
				OR p.network = 2)` + slug() + `
			ORDER BY p.created_at DESC`
		return searchLimited[searchEntityProjectRow](ctx, e, sql, s.args)
	case "issue":
		where := ""
		if e.query != "" {
			where = " AND " + s.issueQuery(e.query, true)
		}
		project, err := inProject("i.project_id")
		if err != nil {
			return nil, err
		}
		sql = `SELECT DISTINCT i.name, i.id, i.sequence_id, p.identifier, i.project_id, i.priority, i.state_id, i.type_id,
				i.created_at
			` + searchIssueFrom + ` WHERE ` + searchIssueLive + where + member() + project + slug() + `
			ORDER BY i.created_at DESC`
		return searchLimited[searchEntityIssueRow](ctx, e, sql, s.args)
	case "cycle", "module":
		table, status := "cycles", searchCycleStatus
		if t == "module" {
			table, status = "modules", "c.status"
		}
		fields := cond("c.name")
		project, err := inProject("c.project_id")
		if err != nil {
			return nil, err
		}
		sql = `SELECT DISTINCT c.name, c.id, c.project_id, p.identifier, ` + status + `, w.slug, c.created_at
			FROM ` + table + ` c JOIN projects p ON p.id = c.project_id JOIN project_members pm ON pm.project_id = p.id
			JOIN workspaces w ON w.id = c.workspace_id
			WHERE c.deleted_at IS NULL` + fields + member() + project + slug() + `
			ORDER BY c.created_at DESC`
		return searchLimited[searchEntityStatusRow](ctx, e, sql, s.args)
	case "page":
		// Public pages only; a page in several of the user's projects is
		// listed once per project (projects__id). Workspace-wide, only
		// global pages.
		fields := cond("pg.name")
		scope := " AND pg.is_global"
		if e.scoped() {
			project, err := inProject("pp.project_id")
			if err != nil {
				return nil, err
			}
			scope = project
		}
		sql = `SELECT DISTINCT pg.name, pg.id, pg.logo_props::text, pp.project_id, w.slug, pg.created_at
			FROM pages pg JOIN project_pages pp ON pp.page_id = pg.id JOIN projects p ON p.id = pp.project_id
			JOIN project_members pm ON pm.project_id = p.id JOIN workspaces w ON w.id = pg.workspace_id
			WHERE pg.deleted_at IS NULL` + fields + ` AND pg.access = 0` + scope + member() + slug() + `
			ORDER BY pg.created_at DESC`
		return searchLimited[searchEntityPageRow](ctx, e, sql, s.args)
	}
	return nil, nil
}

// searchLimited runs sql with the [:count] slice applied.
func searchLimited[T any](ctx context.Context, e entitySearcher, sql string, args []any) (any, error) {
	limit, run, err := e.limit()
	if err != nil {
		return nil, err
	}
	if !run {
		return []T{}, nil
	}
	return searchRows[T](ctx, e.a, sql+limit, args)
}
