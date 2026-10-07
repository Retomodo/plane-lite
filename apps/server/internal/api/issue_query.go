package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// issueQuery builds the SQL of an Issue queryset the way Django joins it.
// What matters is which rows a join produces, so it follows Django's
// rules for reusing joins to multi-valued relations: a filter() call reuses
// only the joins it created itself, while annotations and F() references
// reuse the most recent join of the relation. Every relation join is a LEFT
// JOIN; Django's INNER joins only appear where the conditions reject the
// NULL rows anyway.
//
// The issue row is aliased i, its state s, its project p and workspace w.
type issueQuery struct {
	args  []any
	joins []qJoin
	where []string
	tz    string // the active time zone, for __date lookups
}

type qJoin struct{ alias, key, sql string }

// filterScope is one QuerySet.filter() call: the relation joins it made.
type filterScope map[string]bool

func newScope() filterScope { return filterScope{} }

func (q *issueQuery) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

// issueRelations are the multi-valued relations of an issue, by table: the
// reverse foreign keys (label_issue, issue_assignee, ...) and the first hop
// of the many-to-many fields through them (labels, assignees), which Django
// treats as the same join.
var issueRelations = map[string]bool{
	"issue_labels": true, "issue_assignees": true, "module_issues": true, "cycle_issues": true,
	"issue_mentions": true, "issue_subscribers": true, "intake_issues": true,
}

// rel joins a relation of the issue. sc is the filter() call asking, or nil
// for an annotation or reference.
func (q *issueQuery) rel(table string, sc filterScope) string {
	if sc != nil {
		for _, j := range q.joins {
			if j.key == table && sc[j.alias] {
				return j.alias
			}
		}
	} else {
		for i := len(q.joins) - 1; i >= 0; i-- {
			if q.joins[i].key == table {
				return q.joins[i].alias
			}
		}
	}
	alias := "j" + strconv.Itoa(len(q.joins)+1)
	q.joins = append(q.joins, qJoin{alias, table,
		fmt.Sprintf("LEFT JOIN %s %s ON %s.issue_id = i.id", table, alias, alias)})
	if sc != nil {
		sc[alias] = true
	}
	return alias
}

// fk follows a foreign key from a joined row; such joins are always reused.
func (q *issueQuery) fk(parent, col, table string) string {
	key := table + "|" + parent + "|" + col
	for _, j := range q.joins {
		if j.key == key {
			return j.alias
		}
	}
	alias := "j" + strconv.Itoa(len(q.joins)+1)
	q.joins = append(q.joins, qJoin{alias, key,
		fmt.Sprintf("LEFT JOIN %s %s ON %s.id = %s.%s", table, alias, alias, parent, col)})
	return alias
}

func (q *issueQuery) joinSQL() string {
	var b strings.Builder
	for _, j := range q.joins {
		b.WriteString(" ")
		b.WriteString(j.sql)
	}
	return b.String()
}

func (q *issueQuery) whereSQL() string {
	if len(q.where) == 0 {
		return "TRUE"
	}
	return strings.Join(q.where, " AND ")
}

// filter adds one filter() call's condition; "" adds nothing.
func (q *issueQuery) filter(cond string) {
	if cond != "" {
		q.where = append(q.where, cond)
	}
}

// lookup is one field lookup of a filter: a condition on a column of the
// issue (rel "") or of a related row.
type lookup struct {
	rel string
	col string // column of the relation, or an expression on i/s
	// cond renders the condition on the column expression.
	cond func(col string) string
	// nullable marks a column that may be NULL (via a LEFT join or a
	// nullable field): a negated lookup on it also requires IS NOT NULL.
	nullable bool
	// isnull marks a col IS NULL lookup.
	isnull bool
}

// render is build_filter for a lookup. negated is whether an odd number of
// NOTs enclose it, branchNegated whether any do: a multi-valued lookup
// under a NOT becomes an EXISTS subquery (split_exclude).
func (q *issueQuery) render(l lookup, sc filterScope, negated, branchNegated bool) string {
	if l.rel == "" {
		c := l.cond(l.col)
		if negated && l.nullable && !l.isnull {
			c = "(" + c + " AND " + l.col + " IS NOT NULL)"
		}
		return c
	}
	if branchNegated {
		if l.isnull {
			return fmt.Sprintf("EXISTS(SELECT 1 FROM issues u0 LEFT JOIN %s u1 ON u0.id = u1.issue_id WHERE %s AND u0.id = i.id)",
				l.rel, l.cond("u1."+l.col))
		}
		return fmt.Sprintf("EXISTS(SELECT 1 FROM %s u1 WHERE %s AND u1.issue_id = i.id)", l.rel, l.cond("u1."+l.col))
	}
	return l.cond(q.rel(l.rel, sc) + "." + l.col)
}

// Condition helpers.

func (q *issueQuery) eq(v any, cast string) func(string) string {
	return func(col string) string { return col + " = " + q.arg(v) + cast }
}

func (q *issueQuery) in(vs []string, cast string) func(string) string {
	return func(col string) string {
		if len(vs) == 0 {
			return "FALSE" // EmptyResultSet
		}
		return col + " = ANY(" + q.arg(vs) + "::" + cast + "[])"
	}
}

func isNull(col string) string    { return col + " IS NULL" }
func isNotNull(col string) string { return col + " IS NOT NULL" }

// qNode is a Q object.
type qNode struct {
	op   string // "and", "or", "not" or "" for a leaf
	kids []*qNode
	leaf []lookup // a leaf's lookups, ANDed
	// always marks Q(pk__in=queryset) over the queryset itself: true for
	// every row.
	always bool
}

func (q *issueQuery) renderQ(n *qNode, sc filterScope, negated, branchNegated bool) string {
	switch n.op {
	case "and", "or":
		var parts []string
		for _, k := range n.kids {
			if c := q.renderQ(k, sc, negated, branchNegated); c != "" {
				parts = append(parts, c)
			}
		}
		if len(parts) == 0 {
			return ""
		}
		sep := " AND "
		if n.op == "or" {
			sep = " OR "
		}
		return "(" + strings.Join(parts, sep) + ")"
	case "not":
		c := q.renderQ(n.kids[0], sc, !negated, true)
		if c == "" {
			return ""
		}
		return "NOT (" + c + ")"
	}
	var parts []string
	if n.always {
		parts = append(parts, "TRUE")
	}
	for _, l := range n.leaf {
		parts = append(parts, q.render(l, sc, negated, branchNegated))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// filterError is ComplexFilterBackend's DRF ValidationError body.
func filterError(code, message string) error {
	return httpx.Body(http.StatusBadRequest, map[string]any{"message": message, "code": code})
}

// richFilterKind is how IssueFilterSet declares a filter.
type richFilterKind int

const (
	rfUUID richFilterKind = iota
	rfUUIDIn
	rfChar
	rfCharIn
	rfChoice
	rfDate      // DateFilter on a date column (exact)
	rfDateOf    // DateFilter with lookup "date" on a datetime column
	rfDateRange // BaseRangeFilter: exactly two dates
	rfDateCSV   // DateCSVRangeFilter: date__range on a datetime column
	rfBool
	rfArchived
)

type richFilterDef struct {
	kind richFilterKind
	// rel and col locate the column: rel "" means col is an expression
	// on the issue row.
	rel, col string
	nullable bool
}

// issueFilterSet is IssueFilterSet.base_filters, including the generated
// __exact aliases.
var issueFilterSet = func() map[string]richFilterDef {
	m := map[string]richFilterDef{}
	for name, rel := range map[string][2]string{
		"assignee_id": {"issue_assignees", "assignee_id"}, "cycle_id": {"cycle_issues", "cycle_id"},
		"module_id": {"module_issues", "module_id"}, "mention_id": {"issue_mentions", "mention_id"},
		"label_id": {"issue_labels", "label_id"}, "subscriber_id": {"issue_subscribers", "subscriber_id"},
	} {
		m[name] = richFilterDef{kind: rfUUID, rel: rel[0], col: rel[1]}
		m[name+"__exact"] = m[name]
		m[name+"__in"] = richFilterDef{kind: rfUUIDIn, rel: rel[0], col: rel[1]}
	}
	for name, col := range map[string]string{"created_by_id": "i.created_by_id", "state_id": "i.state_id",
		"project_id": "i.project_id"} {
		nullable := name != "project_id"
		m[name] = richFilterDef{kind: rfUUID, col: col, nullable: nullable}
		m[name+"__exact"] = m[name]
		m[name+"__in"] = richFilterDef{kind: rfUUIDIn, col: col, nullable: nullable}
	}
	m["is_archived"] = richFilterDef{kind: rfArchived}
	m["is_archived__exact"] = m["is_archived"]
	m["state_group"] = richFilterDef{kind: rfChar, col: `s."group"`, nullable: true}
	m["state_group__exact"] = m["state_group"]
	m["state_group__in"] = richFilterDef{kind: rfCharIn, col: `s."group"`, nullable: true}
	for _, f := range []string{"created_at", "updated_at"} {
		m[f] = richFilterDef{kind: rfDateOf, col: "i." + f}
		m[f+"__exact"] = m[f]
		m[f+"__range"] = richFilterDef{kind: rfDateCSV, col: "i." + f}
	}
	for _, f := range []string{"start_date", "target_date"} {
		m[f] = richFilterDef{kind: rfDate, col: "i." + f, nullable: true}
		m[f+"__exact"] = m[f]
		m[f+"__range"] = richFilterDef{kind: rfDateRange, col: "i." + f, nullable: true}
	}
	m["is_draft"] = richFilterDef{kind: rfBool, col: "i.is_draft"}
	m["is_draft__exact"] = m["is_draft"]
	m["priority"] = richFilterDef{kind: rfChoice, col: "i.priority"}
	m["priority__exact"] = m["priority"]
	m["priority__in"] = richFilterDef{kind: rfCharIn, col: "i.priority"}
	return m
}()

// filterNode is a json.loads'ed filter value with dict keys in order.
type filterNode struct {
	raw  jsontext.Value
	keys []string
	vals map[string]jsontext.Value
}

func parseFilterNode(raw jsontext.Value) *filterNode {
	n := &filterNode{raw: raw}
	if raw.Kind() == '{' {
		d := drf.DataFromJSON(raw)
		n.keys = d.Keys()
		n.vals = map[string]jsontext.Value{}
		for _, k := range n.keys {
			v, _ := d.Get(k)
			n.vals[k] = v.Raw()
		}
	}
	return n
}

func (n *filterNode) isDict() bool { return n.raw.Kind() == '{' }

func isLogical(k string) bool {
	switch strings.ToLower(k) {
	case "or", "and", "not":
		return true
	}
	return false
}

// pyFalsy is `not value` for a json.loads'ed value.
func pyFalsy(raw jsontext.Value) bool { return !drf.PyTruthy(drf.JSONValue(raw)) }

// richFilterQ ports ComplexFilterBackend.filter_queryset up to the Q object:
// nil means no filtering.
func (q *issueQuery) richFilterQ(param string) (*qNode, error) {
	if param == "" {
		return nil, nil
	}
	if !jsontext.Value(param).IsValid(jsontext.AllowDuplicateNames(true)) {
		return nil, filterError("invalid_json", "Invalid JSON for 'filter'. Expected a valid JSON object.")
	}
	raw := jsontext.Value(strings.TrimSpace(param))
	if pyFalsy(raw) {
		return nil, nil
	}
	root := parseFilterNode(raw)
	if err := validateFilterNode(root, 1); err != nil {
		return nil, err
	}
	if err := validateFilterFields(root); err != nil {
		return nil, err
	}
	return q.evaluateFilterNode(root)
}

func validateFilterNode(n *filterNode, depth int) error {
	const maxDepth = 5
	if depth > maxDepth {
		return filterError("max_depth_exceeded", fmt.Sprintf("Filter nesting is too deep (max %d); found depth %d", maxDepth, depth))
	}
	if !n.isDict() {
		return filterError("invalid_filter_node", "Each filter node must be a JSON object")
	}
	if len(n.keys) == 0 {
		return filterError("empty_filter_object", "Filter objects must not be empty")
	}
	var logical []string
	for _, k := range n.keys {
		if isLogical(k) {
			logical = append(logical, k)
		}
	}
	if len(logical) > 1 {
		return filterError("multiple_logical_operators", "A filter object cannot contain multiple logical operators at the same level")
	}
	if len(logical) == 1 {
		key := logical[0]
		if len(n.keys) != 1 {
			return filterError("mixed_operator_and_fields", fmt.Sprintf("Cannot mix logical operator '%s' with field keys at the same level", key))
		}
		op := strings.ToLower(key)
		val := n.vals[key]
		if op == "not" {
			child := parseFilterNode(val)
			if !child.isDict() {
				return filterError("invalid_not_child", "'not' must be a single JSON object")
			}
			return validateFilterNode(child, depth+1)
		}
		elems, isList := drf.JSONValue(val).Elems()
		if !isList || len(elems) == 0 {
			return filterError("invalid_operator_children", fmt.Sprintf("'%s' must be a non-empty list of filter objects", op))
		}
		for _, e := range elems {
			child := parseFilterNode(e.Raw())
			if !child.isDict() {
				return filterError("invalid_operator_child_type", fmt.Sprintf("All children of '%s' must be JSON objects", op))
			}
			if err := validateFilterNode(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	for _, k := range n.keys {
		v := drf.JSONValue(n.vals[k])
		if elems, ok := v.Elems(); ok {
			if len(elems) == 0 {
				return filterError("empty_list_value", fmt.Sprintf("List value for '%s' must not be empty", k))
			}
			for _, e := range elems {
				if !isScalar(e) {
					return filterError("non_scalar_list_item", fmt.Sprintf("List value for '%s' must contain only scalar items", k))
				}
			}
			continue
		}
		if !isScalar(v) {
			return filterError("invalid_value_type", fmt.Sprintf("Value for '%s' must be a scalar, null, or list/tuple of scalars", k))
		}
	}
	return nil
}

func isScalar(v drf.Value) bool { return v.Kind() != '{' && v.Kind() != '[' }

// validateFilterFields is _validate_fields: every field named anywhere must
// be a declared filter. Logical keys match in any case here.
func validateFilterFields(n *filterNode) error {
	var walk func(n *filterNode) error
	walk = func(n *filterNode) error {
		if !n.isDict() {
			return nil
		}
		for _, k := range n.keys {
			switch strings.ToLower(k) {
			case "not":
				if err := walk(parseFilterNode(n.vals[k])); err != nil {
					return err
				}
			case "or", "and":
				elems, _ := drf.JSONValue(n.vals[k]).Elems()
				for _, e := range elems {
					if err := walk(parseFilterNode(e.Raw())); err != nil {
						return err
					}
				}
			default:
				if _, ok := issueFilterSet[k]; !ok {
					return filterError("invalid_filter_field", fmt.Sprintf("Filtering on field '%s' is not allowed", k))
				}
			}
		}
		return nil
	}
	return walk(n)
}

// evaluateFilterNode is _evaluate_node. Only the lowercase operator keys
// combine here; "OR" and friends fall through to a leaf the filterset
// ignores.
func (q *issueQuery) evaluateFilterNode(n *filterNode) (*qNode, error) {
	if !n.isDict() {
		return nil, nil
	}
	for _, op := range []string{"or", "and"} {
		if v, ok := n.vals[op]; ok {
			elems, _ := drf.JSONValue(v).Elems()
			node := &qNode{op: op}
			for _, e := range elems {
				child, err := q.evaluateFilterNode(parseFilterNode(e.Raw()))
				if err != nil {
					return nil, err
				}
				if child != nil {
					node.kids = append(node.kids, child)
				}
			}
			return node, nil
		}
	}
	if v, ok := n.vals["not"]; ok {
		child, err := q.evaluateFilterNode(parseFilterNode(v))
		if err != nil || child == nil {
			return nil, err
		}
		return &qNode{op: "not", kids: []*qNode{child}}, nil
	}
	return q.filterLeaf(n)
}

// queryDictValue is what the filterset's form reads for a leaf value: the
// QueryDict holds str() of each list item and .get() returns the last.
func queryDictValue(raw jsontext.Value) string {
	v := drf.JSONValue(raw)
	if elems, ok := v.Elems(); ok {
		last := elems[len(elems)-1]
		if last.IsNull() {
			return "None"
		}
		return drf.PyStr(last)
	}
	if v.IsNull() {
		return ""
	}
	return drf.PyStr(v)
}

const (
	msgFilterUUID  = "Enter a valid UUID."
	msgFilterDate  = "Enter a valid date."
	msgFilterRange = "Range query expects two values."
)

// filterLeaf is _build_leaf_q: the leaf's values bound to IssueFilterSet,
// validated, then build_combined_q.
func (q *issueQuery) filterLeaf(n *filterNode) (*qNode, error) {
	errs := map[string][]string{}
	node := &qNode{}
	for _, k := range n.keys {
		def, ok := issueFilterSet[k]
		if !ok {
			continue // not a declared filter: ignored
		}
		s := queryDictValue(n.vals[k])
		l, always, msg, err := q.boundFilter(def, s)
		if err != nil {
			return nil, err
		}
		if msg != "" {
			errs[k] = []string{msg}
			continue
		}
		if always {
			node.always = true
			continue
		}
		node.leaf = append(node.leaf, l...)
	}
	if len(errs) > 0 {
		return nil, httpx.Body(http.StatusBadRequest, map[string]any{
			"message": "Invalid filter parameters", "code": "invalid_filterset", "errors": errs,
		})
	}
	if !node.always && len(node.leaf) == 0 {
		return &qNode{}, nil
	}
	return node, nil
}

// csv is BaseCSVWidget: "" is an empty list.
func csv(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

// boundFilter cleans one filter value and builds its lookups. always means
// a method filter got an empty value and returned the queryset itself; a
// non-empty msg is the field's validation error.
func (q *issueQuery) boundFilter(def richFilterDef, s string) (ls []lookup, always bool, msg string, err error) {
	at := func(cond func(string) string, extra ...lookup) []lookup {
		return append([]lookup{{rel: def.rel, col: def.col, cond: cond, nullable: def.nullable}}, extra...)
	}
	// The method filters on relations also require a live relation row.
	live := lookup{rel: def.rel, col: "deleted_at", cond: isNull, isnull: true}
	switch def.kind {
	case rfUUID:
		id, ok := formUUID(s)
		if !ok {
			return nil, false, msgFilterUUID, nil
		}
		if def.rel != "" {
			if id == nil {
				return nil, true, "", nil
			}
			return []lookup{{rel: def.rel, col: def.col, cond: q.eq(*id, "::uuid")}, live}, false, "", nil
		}
		if id == nil {
			return []lookup{{col: def.col, cond: isNull, isnull: true}}, false, "", nil
		}
		return at(q.eq(*id, "::uuid")), false, "", nil
	case rfUUIDIn:
		var ids []string
		for _, part := range csv(s) {
			id, ok := formUUID(part)
			if !ok {
				return nil, false, msgFilterUUID, nil
			}
			if id != nil {
				ids = append(ids, id.String())
			}
		}
		if def.rel != "" {
			if s == "" {
				return nil, true, "", nil
			}
			return []lookup{{rel: def.rel, col: def.col, cond: q.in(ids, "uuid")}, live}, false, "", nil
		}
		return at(q.in(ids, "uuid")), false, "", nil
	case rfChar:
		return at(q.eq(s, "")), false, "", nil
	case rfChoice:
		if s != "" && !contains(issuePriorities, s) {
			return nil, false, fmt.Sprintf("Select a valid choice. %s is not one of the available choices.", s), nil
		}
		return at(q.eq(s, "")), false, "", nil
	case rfCharIn:
		return at(q.in(csv(s), "text")), false, "", nil
	case rfDate, rfDateOf:
		d, ok := drf.ParseFormDate(s)
		if !ok {
			return nil, false, msgFilterDate, nil
		}
		col := def.col
		if def.kind == rfDateOf {
			col = q.dateOf(def.col)
		}
		if d == nil {
			return []lookup{{col: col, cond: isNull, isnull: true}}, false, "", nil
		}
		return []lookup{{col: col, cond: q.eq(d.Format(time.DateOnly), "::date"), nullable: def.nullable}}, false, "", nil
	case rfDateRange, rfDateCSV:
		var dates []any // nil for a blank item, which compares as NULL
		for _, part := range csv(s) {
			d, ok := drf.ParseFormDate(part)
			if !ok {
				return nil, false, msgFilterDate, nil
			}
			if d == nil {
				dates = append(dates, nil)
			} else {
				dates = append(dates, d.Format(time.DateOnly))
			}
		}
		if def.kind == rfDateRange && len(dates) > 0 && len(dates) != 2 {
			return nil, false, msgFilterRange, nil
		}
		if len(dates) != 2 {
			return nil, false, "", errViewCrash // BETWEEN needs exactly two values
		}
		col := def.col
		if def.kind == rfDateCSV {
			col = q.dateOf(def.col)
		}
		cond := func(c string) string {
			return c + " BETWEEN " + q.arg(dates[0]) + "::date AND " + q.arg(dates[1]) + "::date"
		}
		return []lookup{{col: col, cond: cond, nullable: def.nullable}}, false, "", nil
	case rfBool:
		b, ok := nullBool(s)
		if !ok {
			return []lookup{{col: def.col, cond: isNull, isnull: true}}, false, "", nil
		}
		return at(q.eq(b, "")), false, "", nil
	case rfArchived:
		b, ok := nullBool(s)
		switch {
		case !ok:
			return nil, true, "", nil
		case b:
			return []lookup{{col: "i.archived_at", cond: isNotNull, isnull: true}}, false, "", nil
		default:
			return []lookup{{col: "i.archived_at", cond: isNull, isnull: true}}, false, "", nil
		}
	}
	return nil, false, "", nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// formUUID is forms.UUIDField.clean: blank is nil.
func formUUID(s string) (*uuid.UUID, bool) {
	s = drf.PyStrip(s)
	if s == "" {
		return nil, true
	}
	id, ok := drf.ParseUUID(s)
	if !ok {
		return nil, false
	}
	return &id, true
}

// nullBool is forms.NullBooleanField.to_python; ok false is None.
func nullBool(s string) (bool, bool) {
	switch s {
	case "True", "true", "1":
		return true, true
	case "False", "false", "0":
		return false, true
	}
	return false, false
}

// dateOf is a datetime column's __date transform in the active time zone.
func (q *issueQuery) dateOf(col string) string {
	return "(" + col + " AT TIME ZONE " + q.arg(q.tz) + ")::date"
}

// Legacy issue_filters (plane.utils.issue_filters, method GET).

var errFilterDetail = httpx.Err(http.StatusBadRequest, "Please provide valid detail")

// legacyUUIDs is filter_valid_uuids over the comma-separated values that
// are not "null".
func legacyItems(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item != "null" {
			out = append(out, item)
		}
	}
	return out
}

func validUUIDs(items []string) []string {
	var out []string
	for _, it := range items {
		if id, ok := drf.ParseUUID(it); ok {
			out = append(out, id.String())
		}
	}
	return out
}

var relativeDate = regexp.MustCompile(`^[0-9]+_(weeks|months)\n?$`)

// legacyFilters ports issue_filters(query_params, "GET") into one filter()
// call's lookups, plus updated_at__gt when extra is set (the list view's
// extra_filters, applied in the same call).
func (q *issueQuery) legacyFilters(params map[string][]string, extra bool) ([]lookup, error) {
	get := func(k string) (string, bool) {
		v, ok := params[k]
		if !ok || len(v) == 0 {
			return "", false
		}
		return v[len(v)-1], true
	}
	// issue_filter is a dict: a later filter replaces a lookup of the same
	// name, so lookups are kept by name.
	var names []string
	byName := map[string]lookup{}
	set := func(name string, l lookup) {
		if _, ok := byName[name]; !ok {
			names = append(names, name)
		}
		byName[name] = l
	}
	direct := func(col string, cond func(string) string) lookup { return lookup{col: col, cond: cond} }
	onRel := func(rel, col string, cond func(string) string) lookup { return lookup{rel: rel, col: col, cond: cond} }

	uuidList := func(key, name string, mk func(ids []string) lookup, none func() lookup) {
		v, ok := get(key)
		if !ok {
			return
		}
		items := legacyItems(v)
		if none != nil && contains(items, "None") {
			set(name+"__isnull", none())
		}
		if ids := validUUIDs(items); len(ids) > 0 {
			set(name+"__in", mk(ids))
		}
	}
	strList := func(key, name string, mk func(vals []string) lookup) {
		v, ok := get(key)
		if !ok {
			return
		}
		items := legacyItems(v)
		if len(items) > 0 && !contains(items, "") {
			set(name, mk(items))
		}
	}

	uuidList("state", "state", func(ids []string) lookup { return direct("i.state_id", q.in(ids, "uuid")) }, nil)
	strList("state_group", "state__group__in", func(vs []string) lookup { return direct(`s."group"`, q.in(vs, "text")) })
	if v, ok := get("estimate_point"); ok {
		items := legacyItems(v)
		if len(items) > 0 && !contains(items, "") {
			var ids []string
			for _, it := range items {
				id, ok := drf.ParseUUID(it)
				if !ok {
					return nil, errFilterDetail // UUIDField.to_python raises ValidationError
				}
				ids = append(ids, id.String())
			}
			set("estimate_point__in", direct("i.estimate_point_id", q.in(ids, "uuid")))
		}
	}
	strList("priority", "priority__in", func(vs []string) lookup { return direct("i.priority", q.in(vs, "text")) })
	uuidList("parent", "parent", func(ids []string) lookup { return direct("i.parent_id", q.in(ids, "uuid")) },
		func() lookup { return direct("i.parent_id", isNull) })
	if _, ok := get("labels"); ok {
		uuidList("labels", "labels", func(ids []string) lookup { return onRel("issue_labels", "label_id", q.in(ids, "uuid")) },
			func() lookup { return onRel("issue_labels", "label_id", isNull) })
		set("label_issue__deleted_at__isnull", onRel("issue_labels", "deleted_at", isNull))
	}
	if _, ok := get("assignees"); ok {
		uuidList("assignees", "assignees", func(ids []string) lookup { return onRel("issue_assignees", "assignee_id", q.in(ids, "uuid")) },
			func() lookup { return onRel("issue_assignees", "assignee_id", isNull) })
		set("issue_assignee__deleted_at__isnull", onRel("issue_assignees", "deleted_at", isNull))
	}
	uuidList("mentions", "issue_mention__mention__id", func(ids []string) lookup {
		return onRel("issue_mentions", "mention_id", q.in(ids, "uuid"))
	}, nil)
	uuidList("created_by", "created_by", func(ids []string) lookup { return direct("i.created_by_id", q.in(ids, "uuid")) },
		func() lookup { return direct("i.created_by_id", isNull) })
	if _, ok := get("logged_by"); ok {
		return nil, errViewCrash // Issue has no logged_by: FieldError
	}
	if v, ok := get("name"); ok && v != "" {
		set("name__icontains", direct("i.name", func(col string) string {
			return "UPPER(" + col + "::text) LIKE UPPER(" + q.arg("%"+likeEscape(v)+"%") + ")"
		}))
	}
	for _, f := range []struct{ key, col string }{
		{"created_at", "i.created_at"}, {"updated_at", "i.updated_at"}, {"start_date", "i.start_date"},
		{"target_date", "i.target_date"}, {"completed_at", "i.completed_at"},
	} {
		v, ok := get(f.key)
		if !ok {
			continue
		}
		parts := strings.Split(v, ",")
		if contains(parts, "") {
			continue
		}
		col := f.col
		if f.key != "start_date" && f.key != "target_date" {
			col = q.dateOf(f.col)
		}
		if err := q.legacyDateFilter(f.key, col, parts, set); err != nil {
			return nil, err
		}
	}
	if _, ok := params["type"]; ok {
		groups := []string{"backlog", "unstarted", "started", "completed", "cancelled"}
		switch v, _ := get("type"); v {
		case "backlog":
			groups = []string{"backlog"}
		case "active":
			groups = []string{"unstarted", "started"}
		}
		set("state__group__in", direct(`s."group"`, q.in(groups, "text")))
	}
	uuidList("project", "project", func(ids []string) lookup { return direct("i.project_id", q.in(ids, "uuid")) }, nil)
	if _, ok := get("cycle"); ok {
		uuidList("cycle", "issue_cycle__cycle_id", func(ids []string) lookup { return onRel("cycle_issues", "cycle_id", q.in(ids, "uuid")) },
			func() lookup { return onRel("cycle_issues", "cycle_id", isNull) })
		set("issue_cycle__deleted_at__isnull", onRel("cycle_issues", "deleted_at", isNull))
	}
	if _, ok := get("module"); ok {
		uuidList("module", "issue_module__module_id", func(ids []string) lookup { return onRel("module_issues", "module_id", q.in(ids, "uuid")) },
			func() lookup { return onRel("module_issues", "module_id", isNull) })
		set("issue_module__deleted_at__isnull", onRel("module_issues", "deleted_at", isNull))
	}
	for _, key := range []string{"intake_status", "inbox_status"} {
		v, ok := get(key)
		if !ok {
			continue
		}
		items := legacyItems(v)
		if len(items) == 0 || contains(items, "") {
			continue
		}
		var nums []int64
		for _, it := range items {
			n, ok := drf.PyIntString(it)
			if !ok || !n.IsInt64() {
				return nil, errViewCrash // IntegerField.get_prep_value raises ValueError
			}
			nums = append(nums, n.Int64())
		}
		set("issue_intake__status__in", onRel("intake_issues", "status", func(col string) string {
			return col + " = ANY(" + q.arg(nums) + "::int[])"
		}))
	}
	if v, ok := get("sub_issue"); ok && v == "false" {
		set("parent__isnull", direct("i.parent_id", isNull))
	}
	if _, ok := get("subscriber"); ok {
		uuidList("subscriber", "issue_subscribers__subscriber_id", func(ids []string) lookup {
			return onRel("issue_subscribers", "subscriber_id", q.in(ids, "uuid"))
		}, nil)
		set("issue_subscribers__deleted_at__isnull", onRel("issue_subscribers", "deleted_at", isNull))
	}
	if v, ok := get("start_target_date"); ok && v == "true" {
		set("target_date__isnull", direct("i.target_date", isNotNull))
		set("start_date__isnull", direct("i.start_date", isNotNull))
	}
	if extra {
		if v, ok := get("updated_at__gt"); ok {
			t, ok := modelDateTime(v, q.loc())
			if !ok {
				return nil, errFilterDetail
			}
			set("updated_at__gt", direct("i.updated_at", func(col string) string { return col + " > " + q.arg(t) }))
		}
	}
	out := make([]lookup, len(names))
	for i, n := range names {
		out[i] = byName[n]
	}
	return out, nil
}

func (q *issueQuery) loc() *time.Location {
	if l, err := time.LoadLocation(q.tz); err == nil {
		return l
	}
	return time.UTC
}

// modelDateTime is models.DateTimeField.to_python on a string: a datetime,
// or a date at midnight; naive values are in the active time zone.
func modelDateTime(s string, loc *time.Location) (time.Time, bool) {
	if t, ok := drf.ParseDateTime(s, loc); ok {
		return t, true
	}
	d, ok := drf.ParseDate(s)
	if !ok {
		return time.Time{}, false
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc), true
}

// legacyDateFilter is issue_filters.date_filter for one field: lookups
// replace one another by name, the last query winning.
func (q *issueQuery) legacyDateFilter(term, col string, queries []string, set func(string, lookup)) error {
	parse := func(s string) (string, error) {
		d, ok := drf.ParseDate(s)
		if !ok {
			return "", errFilterDetail // DateField.to_python raises ValidationError
		}
		return d.Format(time.DateOnly), nil
	}
	cmp := func(op, day string) lookup {
		return lookup{col: col, cond: func(c string) string { return c + " " + op + " " + q.arg(day) + "::date" }}
	}
	for _, query := range queries {
		parts := strings.Split(query, ";")
		if len(parts) >= 2 {
			if relativeDate.MatchString(parts[0]) {
				if len(parts) != 3 {
					continue
				}
				// "2_weeks\n" matches the pattern but its term is "weeks\n",
				// which string_date_filter ignores.
				num, unit, _ := strings.Cut(parts[0], "_")
				if unit != "weeks" && unit != "months" {
					continue
				}
				n, _ := strconv.Atoi(num)
				days := n * 7
				if unit == "months" {
					days = n * 30
				}
				today := time.Now().UTC()
				day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
				if parts[2] == "fromnow" {
					day = day.AddDate(0, 0, days)
				} else {
					day = day.AddDate(0, 0, -days)
				}
				if parts[1] == "after" {
					set(term+"__gte", cmp(">=", day.Format(time.DateOnly)))
				} else {
					set(term+"__lte", cmp("<=", day.Format(time.DateOnly)))
				}
				continue
			}
			day, err := parse(parts[0])
			if err != nil {
				return err
			}
			if contains(parts, "after") {
				set(term+"__gte", cmp(">=", day))
			} else {
				set(term+"__lte", cmp("<=", day))
			}
			continue
		}
		// __contains on a date: its text form contains the value.
		v := parts[0]
		set(term+"__contains", lookup{col: col, cond: func(c string) string {
			return c + "::text LIKE " + q.arg("%"+likeEscape(v)+"%")
		}})
	}
	return nil
}
