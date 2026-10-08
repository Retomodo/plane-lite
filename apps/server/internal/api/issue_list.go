package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// The issue list views: IssueViewSet.list (P/issues/), IssueListEndpoint
// (P/issues/list/) and IssueArchiveViewSet.list (P/archived-issues/). They
// share the filter backends, order_issue_queryset, issue_queryset_grouper
// and the offset paginators, ported here as the SQL Django runs, so joins
// to multi-valued relations multiply and collapse rows exactly as there.

func (q *issueQuery) clone() *issueQuery {
	return &issueQuery{args: slices.Clone(q.args), joins: slices.Clone(q.joins), where: slices.Clone(q.where), tz: q.tz}
}

// issueOrderAllow is ISSUE_ORDER_BY_ALLOWLIST.
var issueOrderAllow = []string{"created_at", "updated_at", "sequence_id", "sort_order", "target_date", "start_date",
	"completed_at", "archived_at", "priority", "state__name", "state__group", "assignees__first_name", "labels__name",
	"issue_module__module__name"}

// issueGroupAllow is ISSUE_GROUP_BY_ALLOWLIST.
var issueGroupAllow = []string{"state_id", "state__group", "priority", "labels__id", "assignees__id",
	"issue_module__module_id", "cycle_id", "project_id", "created_by", "target_date", "start_date"}

// m2mGroups are the group-by fields over multi-valued relations, with the
// relation, its column and the annotation the grouping replaces.
var m2mGroups = map[string]struct{ rel, col, ids string }{
	"labels__id":              {"issue_labels", "label_id", "label_ids"},
	"assignees__id":           {"issue_assignees", "assignee_id", "assignee_ids"},
	"issue_module__module_id": {"module_issues", "module_id", "module_ids"},
}

// issueOrder is order_issue_queryset followed by the paginator's ordering:
// the sort key expression and its direction; agg marks an aggregate key
// (min_values), which groups the query by issue.
type issueOrder struct {
	key  string
	desc bool
	agg  bool
}

var priorityOrder = []string{"urgent", "high", "medium", "low", "none"}
var stateGroupOrder = []string{"backlog", "unstarted", "started", "completed", "cancelled"}

func (q *issueQuery) orderIssues(param string) issueOrder {
	field, desc := sanitizeOrderBy(param, issueOrderAllow, "-created_at")
	cases := func(col string, values []string, def string) string {
		var b strings.Builder
		b.WriteString("CASE")
		for i, v := range values {
			fmt.Fprintf(&b, " WHEN %s = '%s' THEN %d", col, v, i)
		}
		if def != "" {
			b.WriteString(" ELSE " + def)
		}
		b.WriteString(" END")
		return b.String()
	}
	switch field {
	case "priority":
		// "priority" sorts by -priority_order and "-priority" by
		// priority_order.
		return issueOrder{key: cases("i.priority", priorityOrder, ""), desc: !desc}
	case "state__group":
		order := stateGroupOrder
		if desc {
			order = slices.Clone(order)
			slices.Reverse(order)
		}
		return issueOrder{key: cases(`s."group"`, order, "5"), desc: desc}
	case "labels__name":
		return issueOrder{key: "MIN(" + q.fk(q.rel("issue_labels", nil), "label_id", "labels") + ".name)", desc: desc, agg: true}
	case "assignees__first_name":
		return issueOrder{key: "MIN(" + q.fk(q.rel("issue_assignees", nil), "assignee_id", "users") + ".first_name)", desc: desc, agg: true}
	case "issue_module__module__name":
		return issueOrder{key: "MIN(" + q.fk(q.rel("module_issues", nil), "module_id", "modules") + ".name)", desc: desc, agg: true}
	case "state__name":
		return issueOrder{key: "s.name", desc: desc}
	}
	return issueOrder{key: "i." + field, desc: desc}
}

func (o issueOrder) sql(key string) string {
	dir := "ASC"
	if o.desc {
		dir = "DESC"
	}
	return key + " " + dir + " NULLS LAST, i.created_at DESC"
}

// groupExpr is F(field) for a group-by field, resolved against the joins
// so far.
func (q *issueQuery) groupExpr(field string) string {
	if g, ok := m2mGroups[field]; ok {
		return q.rel(g.rel, nil) + "." + g.col
	}
	switch field {
	case "cycle_id":
		return issueCycleSQL
	case "state__group":
		return `s."group"`
	case "created_by":
		return "i.created_by_id"
	}
	return "i." + field
}

// grouper is issue_queryset_grouper's filters: grouping by a relation keeps
// only its live rows, each key in its own filter() call.
func (q *issueQuery) grouper(group, sub string) {
	for _, key := range []string{group, sub} {
		if g, ok := m2mGroups[key]; ok {
			q.filter(q.render(lookup{rel: g.rel, col: "deleted_at", cond: isNull, isnull: true}, newScope(), false, false))
		}
	}
}

// issueList is one list request being answered.
type issueList struct {
	a         *API
	c         *httpx.Ctx
	slug      string
	projectID uuid.UUID
	q         *issueQuery
	// filtered is the queryset before annotations: total_count_queryset,
	// and the source of the date and creator group values.
	filtered *issueQuery
	// archived is the archive view, which passes issue_group_values no
	// queryset.
	archived bool
	from     string // FROM clause before the relation joins
	distinct bool
	// plainCounts marks IssueListEndpoint's count annotations, which are 0
	// rather than NULL for none.
	plainCounts bool
	order       issueOrder
	group, sub  string
	// localTimes renders created_at/updated_at in the user's time zone.
	localTimes bool
	// workspace marks a workspace-level list (the profile issues page),
	// whose issue_group_values gets no project_id.
	workspace bool
}

const issueFrom = ` FROM issues i LEFT JOIN states s ON s.id = i.state_id JOIN projects p ON p.id = i.project_id
	JOIN workspaces w ON w.id = i.workspace_id`

// newIssueList starts the queryset of IssueViewSet.get_queryset (or the
// archive view's).
func (a *API) newIssueList(c *httpx.Ctx, projectID uuid.UUID, archived bool) *issueList {
	l := &issueList{a: a, c: c, slug: c.Param("slug"), projectID: projectID, from: issueFrom, distinct: !archived, archived: archived,
		q: &issueQuery{tz: c.Loc().String()}}
	if archived {
		l.from += ` LEFT JOIN issue_types t ON t.id = i.type_id`
		l.q.filter(`i.deleted_at IS NULL AND (i.type_id IS NULL OR NOT t.is_epic) AND i.archived_at IS NOT NULL`)
	} else {
		l.q.filter(issueBaseWhere)
	}
	l.q.filter("i.project_id = " + l.q.arg(projectID) + " AND w.slug = " + l.q.arg(l.slug))
	return l
}

// issueBaseWhere is Issue.issue_objects on the base joins.
const issueBaseWhere = `i.deleted_at IS NULL AND s."group" IS DISTINCT FROM 'triage' AND i.archived_at IS NULL
	AND p.archived_at IS NULL AND NOT i.is_draft`

// applyFilters runs the rich filters, then the legacy ones (each a filter()
// call), as the views do.
func (l *issueList) applyFilters(extra bool) error {
	q := l.q
	node, err := q.richFilterQ(l.c.Query("filters"))
	if err != nil {
		return err
	}
	if node != nil {
		q.filter(q.renderQ(node, newScope(), false, false))
	}
	lookups, err := q.legacyFilters(l.c.QueryValues(), extra)
	if err != nil {
		return err
	}
	sc := newScope()
	var conds []string
	for _, lk := range lookups {
		conds = append(conds, q.render(lk, sc, false, false))
	}
	if len(conds) > 0 {
		q.filter(strings.Join(conds, " AND "))
	}
	return nil
}

func (l *issueList) sel(q *issueQuery) string {
	if l.distinct {
		return "SELECT DISTINCT "
	}
	return "SELECT "
}

func (l *issueList) body(q *issueQuery) string {
	return l.from + q.joinSQL() + " WHERE " + q.whereSQL()
}

// groupBy is the GROUP BY an aggregate order key brings: the issue and
// every other selected column from a joined row.
func (l *issueList) groupBy(extra ...string) string {
	if !l.order.agg {
		return ""
	}
	return " GROUP BY " + strings.Join(append([]string{"i.id", `s."group"`}, extra...), ", ")
}

// valueCol is one column of issue_on_results's .values().
type valueCol struct{ name, expr string }

func (l *issueList) counts() (subs, attachments, links string) {
	if l.plainCounts {
		return issueSubIssuesCountSQL, issueAttachmentCountSQL, issueLinkCountSQL
	}
	return nullIfZero(issueSubIssuesCountSQL), nullIfZero(issueAttachmentCountSQL), nullIfZero(issueLinkCountSQL)
}

// values lists the result columns: the required fields, the id arrays not
// replaced by a grouping, then the m2m group fields.
func (l *issueList) values() []valueCol {
	subs, attachments, links := l.counts()
	cols := []valueCol{{"id", "i.id"}, {"name", "i.name"}, {"state_id", "i.state_id"}, {"sort_order", "i.sort_order"},
		{"completed_at", "i.completed_at"}, {"estimate_point", "i.estimate_point_id"}, {"priority", "i.priority"},
		{"start_date", "i.start_date"}, {"target_date", "i.target_date"}, {"sequence_id", "i.sequence_id"},
		{"project_id", "i.project_id"}, {"parent_id", "i.parent_id"}, {"cycle_id", issueCycleSQL},
		{"sub_issues_count", subs}, {"created_at", "i.created_at"}, {"updated_at", "i.updated_at"},
		{"created_by", "i.created_by_id"}, {"updated_by", "i.updated_by_id"}, {"attachment_count", attachments},
		{"link_count", links}, {"is_draft", "i.is_draft"}, {"archived_at", "i.archived_at"}, {"state__group", `s."group"`}}
	arrays := map[string]string{"assignee_ids": issueAssigneeIDsSQL, "label_ids": issueLabelIDsSQL, "module_ids": issueModuleIDsSQL}
	var grouped []valueCol
	for _, name := range []string{"assignee_ids", "label_ids", "module_ids"} {
		replaced := ""
		for _, key := range []string{l.group, l.sub} {
			if g, ok := m2mGroups[key]; ok && g.ids == name {
				replaced = key
			}
		}
		if replaced == "" {
			cols = append(cols, valueCol{name, arrays[name]})
		}
	}
	for _, key := range []string{l.group, l.sub} {
		if _, ok := m2mGroups[key]; ok {
			grouped = append(grouped, valueCol{key, l.q.groupExpr(key)})
		}
	}
	return append(cols, grouped...)
}

func colsSQL(cols []valueCol) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = c.expr + ` AS "` + c.name + `"`
	}
	return strings.Join(parts, ", ")
}

// issueRow is one dict of the .values() results.
type issueRow struct {
	id     uuid.UUID
	fields map[string]any
	keys   []string
}

func (r *issueRow) set(k string, v any) {
	if _, ok := r.fields[k]; !ok {
		r.keys = append(r.keys, k)
	}
	r.fields[k] = v
}

func (r *issueRow) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range r.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := httpx.Marshal(k, nil)
		b.Write(key)
		b.WriteByte(':')
		v, err := httpx.Marshal(r.fields[k], nil)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

// scanRows reads .values() rows; extra counts trailing columns that are
// read but not part of the dict.
func (l *issueList) scanRows(ctx context.Context, cols []valueCol, extra int, sql string, args []any) ([]*issueRow, error) {
	rows, err := l.a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*issueRow
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := &issueRow{fields: map[string]any{}}
		for i, c := range cols {
			r.set(c.name, l.jsonValue(c.name, vals[i]))
		}
		r.id = uuid.UUID(vals[0].([16]byte))
		out = append(out, r)
	}
	_ = extra
	return out, rows.Err()
}

// jsonValue converts a scanned column as DRF renders the .values() dict.
func (l *issueList) jsonValue(name string, v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case [16]byte:
		return uuid.UUID(x)
	case []any:
		ids := make([]uuid.UUID, 0, len(x))
		for _, e := range x {
			ids = append(ids, uuid.UUID(e.([16]byte)))
		}
		return ids
	case time.Time:
		switch name {
		case "start_date", "target_date", "archived_at":
			return httpx.Date{Time: x}
		case "created_at", "updated_at":
			if l.localTimes {
				return x
			}
		}
		return utcTime(x)
	case int64:
		return int(x)
	case int32:
		return int(x)
	}
	return v
}

// pyStr is str() of a group value as the paginators key groups by it.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case uuid.UUID:
		return x.String()
	case httpx.Date:
		return x.Format(time.DateOnly)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// listIssues ports IssueViewSet.list.
func (a *API) listIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var guestViewAll bool
	if err := a.db.QueryRow(ctx, `SELECT p.guest_view_all_features FROM projects p JOIN workspaces w ON w.id = p.workspace_id
		WHERE p.id = $1 AND w.slug = $2 AND p.deleted_at IS NULL`, projectID, slug).Scan(&guestViewAll); err != nil {
		return err
	}
	l := a.newIssueList(c, projectID, false)
	if err := l.applyFilters(true); err != nil {
		return err
	}
	l.filtered = l.q.clone()
	l.prepare(c.Query("order_by"), c.Query("group_by"), c.Query("sub_group_by"))
	a.recordVisit(ctx, slug, "project", projectID, c.User.ID, &projectID)
	guest, err := a.guestBlocked(ctx, slug, projectID, c.User.ID, nil, guestViewAll)
	if err != nil {
		return err
	}
	if guest {
		for _, q := range []*issueQuery{l.q, l.filtered} {
			q.filter("i.created_by_id = " + q.arg(c.User.ID))
		}
	}
	return l.paginate(ctx)
}

// listArchivedIssues ports IssueArchiveViewSet.list.
func (a *API) listArchivedIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	l := a.newIssueList(c, projectID, true)
	show := c.Query("show_sub_issues")
	if c.HasQuery("show_sub_issues") && show != "true" {
		l.q.filter("i.parent_id IS NULL")
	}
	if err := l.applyFilters(false); err != nil {
		return err
	}
	l.filtered = l.q.clone()
	l.prepare(c.Query("order_by"), c.Query("group_by"), c.Query("sub_group_by"))
	return l.paginate(c.Context())
}

// prepare applies the annotations' joins: the order key, then the grouper.
func (l *issueList) prepare(orderBy, group, sub string) {
	if !l.c.HasQuery("order_by") {
		orderBy = "-created_at"
	}
	l.order = l.q.orderIssues(orderBy)
	l.group, l.sub = group, sub
	l.q.grouper(group, sub)
}

// paginate is BasePaginator.paginate with the paginator the view picks.
func (l *issueList) paginate(ctx context.Context) error {
	if l.group != "" {
		if l.sub != "" && l.group == l.sub {
			return httpx.Err(http.StatusBadRequest, "Group by and sub group by cannot have same parameters")
		}
		return l.paginateGrouped(ctx)
	}
	l.sub = ""
	page, err := parseOffsetPage(l.c)
	if err != nil {
		return err
	}
	return l.paginateFlat(ctx, page)
}

// paginateFlat is OffsetPaginator.get_result over issue_on_results.
func (l *issueList) paginateFlat(ctx context.Context, page *offsetPage) error {
	q := l.q
	cols := l.values()
	order := l.order.sql("sort_key")
	gb := l.groupBy()

	// total_count_queryset: evaluated (if non-empty) and counted.
	var total int64
	f := l.filtered
	countSQL := "SELECT count(*) FROM (" + l.sel(f) + "i.id" + l.body(f) + ") x"
	if err := l.a.db.QueryRow(ctx, countSQL, f.args...).Scan(&total); err != nil {
		return err
	}

	stop := page.offset + page.limit + 1
	if page.offset == math.MaxInt64 {
		stop = math.MaxInt64
	}
	pq := q.clone()
	limitSQL := " LIMIT " + pq.arg(stop-page.offset) + " OFFSET " + pq.arg(page.offset)
	var ahead int64
	nextSQL := "SELECT count(*) FROM (" + l.sel(pq) + "i.id, " + l.order.key + " AS sort_key, i.created_at" + l.body(pq) + gb +
		" ORDER BY " + order + limitSQL + ") x"
	if err := l.a.db.QueryRow(ctx, nextSQL, pq.args...).Scan(&ahead); err != nil {
		return err
	}

	rq := q.clone()
	pageSQL := l.sel(rq) + colsSQL(cols) + ", " + l.order.key + " AS sort_key" + l.body(rq) + gb + " ORDER BY " + order +
		" LIMIT " + rq.arg(page.limit) + " OFFSET " + rq.arg(page.offset)
	rows, err := l.scanRows(ctx, cols, 1, pageSQL, rq.args)
	if err != nil {
		return err
	}
	results := make([]*issueRow, len(rows))
	copy(results, rows)
	res := page.response(results, int(ahead), total)
	res.Count = len(rows)
	return l.c.JSON(http.StatusOK, res)
}

// groupValues is issue_group_values: the groups a grouped page lists.
func (l *issueList) groupValues(ctx context.Context, field string) ([]string, error) {
	var sql string
	args := []any{l.slug, l.projectID}
	inProject := func(col string) string { return " AND " + col + " = $2" }
	if l.workspace {
		args = args[:1]
		inProject = func(string) string { return "" }
	}
	none := false
	switch field {
	case "state_id":
		sql = `SELECT st.id::text FROM states st JOIN workspaces w ON w.id = st.workspace_id WHERE NOT st.is_triage
			AND w.slug = $1` + inProject("st.project_id") + ` AND st.deleted_at IS NULL AND st."group" <> 'triage'`
	case "labels__id", "issue_module__module_id", "cycle_id":
		table := map[string]string{"labels__id": "labels", "issue_module__module_id": "modules", "cycle_id": "cycles"}[field]
		sql = `SELECT x.id::text FROM ` + table + ` x JOIN workspaces w ON w.id = x.workspace_id
			WHERE w.slug = $1` + inProject("x.project_id") + ` AND x.deleted_at IS NULL`
		none = true
	case "assignees__id":
		sql = `SELECT pm.member_id::text FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
			WHERE w.slug = $1 AND pm.project_id = $2 AND pm.is_active AND pm.deleted_at IS NULL`
		if l.workspace {
			sql = `SELECT wm.member_id::text FROM workspace_members wm JOIN workspaces w ON w.id = wm.workspace_id
			WHERE w.slug = $1 AND wm.is_active AND wm.deleted_at IS NULL`
		}
	case "project_id":
		sql = `SELECT x.id::text FROM projects x JOIN workspaces w ON w.id = x.workspace_id
			WHERE w.slug = $1 AND x.deleted_at IS NULL`
		args = args[:1]
	case "priority":
		return []string{"low", "medium", "high", "urgent", "none"}, nil
	case "state__group":
		return slices.Clone(stateGroupOrder), nil
	case "target_date", "start_date", "created_by":
		if l.archived {
			return nil, errViewCrash // None.values_list
		}
		col := "i." + field
		if field == "created_by" {
			col = "i.created_by_id"
		}
		f := l.filtered.clone()
		if !l.workspace {
			f.filter("i.project_id = " + f.arg(l.projectID))
		}
		sql = "SELECT DISTINCT " + col + "::text" + l.body(f)
		args = f.args
	default:
		return nil, nil
	}
	rows, err := l.a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	vals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var s *string
		err := row.Scan(&s)
		if s == nil {
			return "None", err
		}
		return *s, err
	})
	if err != nil {
		return nil, err
	}
	if none {
		vals = append(vals, "None")
	}
	return vals, nil
}

// countFilter is the grouped paginators' count_filter as SQL, joining the
// issue's intake row the way an annotation does.
func countFilter(q *issueQuery) string {
	ii := q.rel("intake_issues", nil)
	return fmt.Sprintf(`count(DISTINCT i.id) FILTER (WHERE (%[1]s.status IN (1, -1, 2) OR %[1]s.id IS NULL)
		AND i.archived_at IS NULL AND NOT i.is_draft)`, ii)
}

// groupTotals counts issues per group (and sub group) with count_filter.
func (l *issueList) groupTotals(ctx context.Context, exprs ...string) (map[string]int64, error) {
	q := l.q.clone()
	cnt := countFilter(q)
	keys := make([]string, len(exprs))
	for i, e := range exprs {
		keys[i] = "(" + e + ")::text"
	}
	sql := "SELECT " + strings.Join(keys, ", ") + ", " + cnt + l.body(q) + " GROUP BY " + strings.Join(exprs, ", ")
	rows, err := l.a.db.Query(ctx, sql, q.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		ks := make([]*string, len(exprs))
		dst := make([]any, 0, len(exprs)+1)
		for i := range ks {
			dst = append(dst, &ks[i])
		}
		var n int64
		dst = append(dst, &n)
		if err := rows.Scan(dst...); err != nil {
			return nil, err
		}
		parts := make([]string, len(ks))
		for i, k := range ks {
			parts[i] = "None"
			if k != nil {
				parts[i] = *k
			}
		}
		out[strings.Join(parts, "\x00")] = n
	}
	return out, rows.Err()
}

// groupedPage is the cursor arithmetic of the grouped paginators, which
// use the cursor's value rather than the clamped limit.
type groupedPage struct {
	limit        int64
	page         *big.Int
	offset, stop any // int64, or float64 for a float cursor
	offsetF      float64
	stopF        float64
}

func parseGroupedPage(c *httpx.Ctx) (*groupedPage, error) {
	pp, err := parsePageParams(c)
	if err != nil {
		return nil, err
	}
	if !pp.perPage.IsInt64() || !pp.page.IsInt64() {
		return nil, errViewCrash
	}
	limit := min(pp.perPage.Int64(), maxPageLimit)
	page := pp.page.Int64()
	g := &groupedPage{limit: limit, page: pp.page}
	if pp.valueFloat {
		value := math.NaN()
		if pp.value != nil {
			value, _ = pp.value.Float64()
		}
		offset := float64(page) * value
		if offset < 0 {
			return nil, httpx.Detail(http.StatusBadRequest, "Error in parsing")
		}
		step := value
		if value == 0 {
			step = float64(limit)
		}
		g.offsetF, g.stopF = offset, offset+step+1
		g.offset, g.stop = g.offsetF, g.stopF
		return g, nil
	}
	vi, _ := pp.value.Int(nil)
	if !vi.IsInt64() {
		return nil, errViewCrash
	}
	value := vi.Int64()
	offset := page * value
	if offset < 0 {
		return nil, httpx.Detail(http.StatusBadRequest, "Error in parsing")
	}
	step := value
	if value == 0 {
		step = limit
	}
	g.offset, g.stop = offset, offset+step+1
	g.offsetF, g.stopF = float64(offset), float64(offset+step+1)
	return g, nil
}

// paginateGrouped is GroupedOffsetPaginator / SubGroupedOffsetPaginator.
func (l *issueList) paginateGrouped(ctx context.Context) error {
	groupFields, err := l.groupValues(ctx, l.group)
	if err != nil {
		return err
	}
	if l.sub != "" {
		if _, err := l.groupValues(ctx, l.sub); err != nil {
			return err
		}
	}
	page, err := parseGroupedPage(l.c)
	if err != nil {
		return err
	}
	if !slices.Contains(issueGroupAllow, l.group) {
		return httpx.Detail(http.StatusBadRequest, "Invalid group_by field: "+l.group)
	}
	if l.sub != "" && !slices.Contains(issueGroupAllow, l.sub) {
		return httpx.Detail(http.StatusBadRequest, "Invalid sub_group_by field: "+l.sub)
	}

	q := l.q
	gexpr := q.groupExpr(l.group)
	partition := []string{gexpr}
	var sexpr string
	if l.sub != "" {
		sexpr = q.groupExpr(l.sub)
		partition = append(partition, sexpr)
	}
	window := "ROW_NUMBER() OVER (PARTITION BY " + strings.Join(partition, ", ") + " ORDER BY " + l.order.sql(l.order.key) + ")"
	gb := l.groupBy(partition...)
	cols := l.values()

	// The rows CursorResult counts: distinct over the issue, its annotations
	// and the row number.
	numbered := func(q *issueQuery) string {
		return l.sel(q) + "i.id, " + l.order.key + " AS sort_key, " + window + " AS rn" + l.body(q) + gb
	}
	var hits, count int64
	var more bool
	cq := q.clone()
	if err := l.a.db.QueryRow(ctx, "SELECT count(*) FROM ("+numbered(cq)+") x", cq.args...).Scan(&hits); err != nil {
		return err
	}
	cq = q.clone()
	if err := l.a.db.QueryRow(ctx, "SELECT count(*) FROM ("+numbered(cq)+") x WHERE rn > "+cq.arg(page.offset)+
		" AND rn < "+cq.arg(page.stop), cq.args...).Scan(&count); err != nil {
		return err
	}
	cq = q.clone()
	if err := l.a.db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ("+numbered(cq)+") x WHERE rn >= "+cq.arg(page.stop)+")",
		cq.args...).Scan(&more); err != nil {
		return err
	}
	var maxHits int64
	if count > 0 {
		mq := q.clone()
		cnt := countFilter(mq)
		var top int64
		err := l.a.db.QueryRow(ctx, "SELECT "+cnt+" AS c"+l.body(mq)+" GROUP BY "+gexpr+" ORDER BY c DESC LIMIT 1",
			mq.args...).Scan(&top)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if page.limit == 0 {
			return errViewCrash // ZeroDivisionError
		}
		maxHits = int64(math.Ceil(float64(top) / float64(page.limit)))
	}

	rq := q.clone()
	inner := l.sel(rq) + colsSQL(cols) + ", (" + gexpr + ")::text AS gkey, "
	if sexpr != "" {
		inner += "(" + sexpr + ")::text AS skey, "
	} else {
		inner += "NULL::text AS skey, "
	}
	inner += l.order.key + " AS sort_key, " + window + " AS rn" + l.body(rq) + gb
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = `"` + c.name + `"`
	}
	pageSQL := "SELECT " + strings.Join(names, ", ") + ", gkey, skey FROM (" + inner + ") x WHERE rn > " + rq.arg(page.offset) +
		" AND rn < " + rq.arg(page.stop) + " ORDER BY " + strings.Replace(l.order.sql("sort_key"), "i.created_at", "x.created_at", 1)
	rows, err := l.a.db.Query(ctx, pageSQL, rq.args...)
	if err != nil {
		return err
	}
	var results []groupedRow
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			rows.Close()
			return err
		}
		r := &issueRow{fields: map[string]any{}, id: uuid.UUID(vals[0].([16]byte))}
		for i, c := range cols {
			r.set(c.name, l.jsonValue(c.name, vals[i]))
		}
		k := groupedRow{row: r, gkey: "None", skey: "None"}
		if s, ok := vals[len(cols)].(string); ok {
			k.gkey = s
		}
		if s, ok := vals[len(cols)+1].(string); ok {
			k.skey = s
		}
		results = append(results, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var processed any = map[string]any{}
	if len(results) > 0 {
		if l.sub == "" {
			totals, err := l.groupTotals(ctx, gexpr)
			if err != nil {
				return err
			}
			total := func(k string) int64 {
				if n, ok := totals[k]; ok && n == 0 {
					return 1
				} else if ok {
					return n
				}
				return 0
			}
			if g, ok := m2mGroups[l.group]; ok {
				// __query_multi_grouper: only the groups on the page, each
				// issue in all of its groups.
				groupIDs := map[uuid.UUID][]string{}
				for _, r := range results {
					if !slices.Contains(groupIDs[r.row.id], r.gkey) {
						groupIDs[r.row.id] = append(groupIDs[r.row.id], r.gkey)
					}
				}
				out := map[string]any{}
				lists := map[string][]*issueRow{}
				var order []string
				for _, r := range results {
					ids := groupIDs[r.row.id]
					if slices.Contains(ids, "None") {
						r.row.set(g.ids, []string{})
					} else {
						r.row.set(g.ids, ids)
					}
					for _, gid := range ids {
						if !slices.ContainsFunc(lists[gid], func(x *issueRow) bool { return x.id == r.row.id }) {
							if _, seen := lists[gid]; !seen {
								order = append(order, gid)
							}
							lists[gid] = append(lists[gid], r.row)
						}
					}
				}
				for _, gid := range order {
					var tr any // total_group_dict.get(): None for a group it lacks
					if _, ok := totals[gid]; ok {
						tr = total(gid)
					}
					out[gid] = map[string]any{"results": lists[gid], "total_results": tr}
				}
				processed = out
			} else {
				out := map[string]any{}
				lists := map[string][]*issueRow{}
				for _, f := range groupFields {
					lists[f] = []*issueRow{}
				}
				for _, r := range results {
					if _, ok := lists[r.gkey]; ok {
						lists[r.gkey] = append(lists[r.gkey], r.row)
					}
				}
				for _, f := range groupFields {
					out[f] = map[string]any{"results": lists[f], "total_results": total(f)}
				}
				processed = out
			}
		} else {
			p, err := l.subGrouped(ctx, gexpr, sexpr, groupFields, results)
			if err != nil {
				return err
			}
			processed = p
		}
	}

	group := l.group
	var sub *string
	if l.sub != "" {
		sub = &l.sub
	}
	res := &pageResponse{
		GroupedBy: &group, SubGroupedBy: sub, TotalCount: hits,
		NextCursor:      fmt.Sprintf("%d:%s:0", page.limit, new(big.Int).Add(page.page, big.NewInt(1))),
		PrevCursor:      fmt.Sprintf("%d:%s:1", page.limit, new(big.Int).Sub(page.page, big.NewInt(1))),
		NextPageResults: more, PrevPageResults: page.page.Sign() > 0,
		Count: int(count), TotalPages: maxHits, TotalResults: hits, Results: processed,
	}
	return l.c.JSON(http.StatusOK, res)
}

type groupedRow struct {
	row        *issueRow
	gkey, skey string
}

// subGrouped is SubGroupedOffsetPaginator.process_results.
func (l *issueList) subGrouped(ctx context.Context, gexpr, sexpr string, groupFields []string, results []groupedRow) (any, error) {
	groupTotals, err := l.groupTotals(ctx, gexpr)
	if err != nil {
		return nil, err
	}
	subTotals, err := l.groupTotals(ctx, gexpr, sexpr)
	if err != nil {
		return nil, err
	}
	subsOf := map[string]map[string]int64{}
	for k, n := range subTotals {
		g, s, _ := strings.Cut(k, "\x00")
		if subsOf[g] == nil {
			subsOf[g] = map[string]int64{}
		}
		subsOf[g][s] = n
	}
	type cell struct {
		results []*issueRow
		total   int64
	}
	type group struct {
		subs  map[string]*cell
		total int64
	}
	groups := map[string]*group{}
	for _, f := range groupFields {
		gr := &group{subs: map[string]*cell{}}
		if n, ok := groupTotals[f]; ok {
			gr.total = n
			if n == 0 {
				gr.total = 1
			}
		}
		for s, n := range subsOf[f] {
			gr.subs[s] = &cell{results: []*issueRow{}, total: n}
		}
		groups[f] = gr
	}
	gm, gMulti := m2mGroups[l.group]
	sm, sMulti := m2mGroups[l.sub]
	if gMulti || sMulti {
		groupIDs := map[uuid.UUID][]string{}
		subIDs := map[uuid.UUID][]string{}
		for _, r := range results {
			if gMulti && !slices.Contains(groupIDs[r.row.id], r.gkey) {
				groupIDs[r.row.id] = append(groupIDs[r.row.id], r.gkey)
			}
			if sMulti && !slices.Contains(subIDs[r.row.id], r.skey) {
				subIDs[r.row.id] = append(subIDs[r.row.id], r.skey)
			}
		}
		for _, r := range results {
			gr, ok := groups[r.gkey]
			if !ok {
				continue
			}
			c, ok := gr.subs[r.skey]
			if !ok {
				continue
			}
			// Each row is its own dict: copy the issue's for this cell.
			row := &issueRow{id: r.row.id, fields: map[string]any{}, keys: slices.Clone(r.row.keys)}
			for k, v := range r.row.fields {
				row.fields[k] = v
			}
			if gMulti {
				ids := groupIDs[r.row.id]
				if slices.Contains(ids, "None") {
					row.set(gm.ids, []string{})
				} else {
					row.set(gm.ids, ids)
				}
			}
			if sMulti {
				ids := subIDs[r.row.id]
				if slices.Contains(ids, "None") {
					row.set(sm.ids, []string{})
				} else {
					row.set(sm.ids, ids)
				}
			}
			c.results = append(c.results, row)
		}
	} else {
		for _, r := range results {
			gr, ok := groups[r.gkey]
			if !ok {
				return nil, errViewCrash // KeyError
			}
			c, ok := gr.subs[r.skey]
			if !ok {
				return nil, errViewCrash
			}
			c.results = append(c.results, r.row)
		}
	}
	out := map[string]any{}
	for name, gr := range groups {
		subs := map[string]any{}
		for s, c := range gr.subs {
			subs[s] = map[string]any{"results": c.results, "total_results": c.total}
		}
		out[name] = map[string]any{"results": subs, "total_results": gr.total}
	}
	return out, nil
}

// listIssuesByID ports IssueListEndpoint.get.
func (a *API) listIssuesByID(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	raw := c.Query("issues")
	if raw == "" {
		return httpx.Err(http.StatusBadRequest, "Issues are required")
	}
	var ids []string
	for _, s := range strings.Split(raw, ",") {
		if s == "" {
			continue
		}
		id, ok := drf.ParseUUID(s)
		if !ok {
			return errFilterDetail
		}
		ids = append(ids, id.String())
	}
	l := a.newIssueList(c, projectID, false)
	l.plainCounts, l.localTimes = true, true
	l.q.filter("i.id = ANY(" + l.q.arg(ids) + "::uuid[])")
	var guest bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		JOIN projects x ON x.id = pm.project_id
		WHERE w.slug = $1 AND pm.project_id = $2 AND pm.member_id = $3 AND pm.role = 5 AND pm.is_active
			AND pm.deleted_at IS NULL AND NOT x.guest_view_all_features)`, slug, projectID, c.User.ID).Scan(&guest); err != nil {
		return err
	}
	if guest {
		l.q.filter("i.created_by_id = " + l.q.arg(c.User.ID))
	}
	if err := l.applyFilters(false); err != nil {
		return err
	}
	l.q.filter("s.deleted_at IS NULL")
	l.prepare(c.Query("order_by"), c.Query("group_by"), c.Query("sub_group_by"))
	a.recordVisit(ctx, slug, "project", projectID, c.User.ID, &projectID)
	for _, key := range []string{l.group, l.sub} {
		if _, ok := m2mGroups[key]; ok {
			return errViewCrash // .values() names an id array the grouper left out
		}
	}
	subs, attachments, links := l.counts()
	cols := []valueCol{{"id", "i.id"}, {"name", "i.name"}, {"state_id", "i.state_id"}, {"sort_order", "i.sort_order"},
		{"completed_at", "i.completed_at"}, {"estimate_point", "i.estimate_point_id"}, {"priority", "i.priority"},
		{"start_date", "i.start_date"}, {"target_date", "i.target_date"}, {"sequence_id", "i.sequence_id"},
		{"project_id", "i.project_id"}, {"parent_id", "i.parent_id"}, {"cycle_id", issueCycleSQL},
		{"module_ids", issueModuleIDsSQL}, {"label_ids", issueLabelIDsSQL}, {"assignee_ids", issueAssigneeIDsSQL},
		{"sub_issues_count", subs}, {"created_at", "i.created_at"}, {"updated_at", "i.updated_at"},
		{"created_by", "i.created_by_id"}, {"updated_by", "i.updated_by_id"}, {"attachment_count", attachments},
		{"link_count", links}, {"is_draft", "i.is_draft"}, {"archived_at", "i.archived_at"}, {"deleted_at", "i.deleted_at"}}
	q := l.q
	sql := "SELECT DISTINCT " + colsSQL(cols) + ", " + l.order.key + " AS sort_key" + l.body(q) + l.groupBy() +
		" ORDER BY " + l.order.sql("sort_key")
	rows, err := l.scanRows(ctx, cols, 1, sql, q.args)
	if err != nil {
		return err
	}
	if rows == nil {
		rows = []*issueRow{}
	}
	return c.JSON(http.StatusOK, rows)
}
