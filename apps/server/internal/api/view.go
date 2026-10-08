package api

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// Saved views (plane/app/views/view/base.py): IssueViewViewSet (P/views/)
// and WorkspaceViewViewSet (W/views/), both rendering IssueViewSerializer.

// viewOut is IssueViewSerializer: the pk, the declared is_favorite (only
// where the queryset annotates it), the model fields, then the foreign
// keys.
type viewOut struct {
	ID                uuid.UUID      `json:"id"`
	IsFavorite        *bool          `json:"is_favorite,omitempty"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         *time.Time     `json:"deleted_at"`
	Name              string         `json:"name"`
	Description       string         `json:"description"`
	Query             jsontext.Value `json:"query"`
	Filters           jsontext.Value `json:"filters"`
	DisplayFilters    jsontext.Value `json:"display_filters"`
	DisplayProperties jsontext.Value `json:"display_properties"`
	RichFilters       jsontext.Value `json:"rich_filters"`
	Access            int            `json:"access"`
	SortOrder         float64        `json:"sort_order"`
	LogoProps         jsontext.Value `json:"logo_props"`
	IsLocked          bool           `json:"is_locked"`
	ArchivedAt        *time.Time     `json:"archived_at"`
	CreatedBy         *uuid.UUID     `json:"created_by"`
	UpdatedBy         *uuid.UUID     `json:"updated_by"`
	Workspace         uuid.UUID      `json:"workspace"`
	Project           *uuid.UUID     `json:"project"`
	OwnedBy           uuid.UUID      `json:"owned_by"`
}

const viewCols = `v.id, v.created_at, v.updated_at, v.deleted_at, v.name, v.description, v.query::text, v.filters::text,
	v.display_filters::text, v.display_properties::text, v.rich_filters::text, v.access, v.sort_order, v.logo_props::text,
	v.is_locked, v.archived_at, v.created_by_id, v.updated_by_id, v.workspace_id, v.project_id, v.owned_by_id`

// scanView reads viewCols, plus is_favorite when favorite is set.
func scanView(row pgx.Row, favorite bool) (*viewOut, error) {
	var (
		v                                          viewOut
		query, filters, display, props, rich, logo string
		fav                                        bool
	)
	dst := []any{&v.ID, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt, &v.Name, &v.Description, &query, &filters, &display,
		&props, &rich, &v.Access, &v.SortOrder, &logo, &v.IsLocked, &v.ArchivedAt, &v.CreatedBy, &v.UpdatedBy, &v.Workspace,
		&v.Project, &v.OwnedBy}
	if favorite {
		dst = append(dst, &fav)
	}
	if err := row.Scan(dst...); err != nil {
		return nil, err
	}
	v.Query, v.Filters, v.DisplayFilters = jsontext.Value(query), jsontext.Value(filters), jsontext.Value(display)
	v.DisplayProperties, v.RichFilters, v.LogoProps = jsontext.Value(props), jsontext.Value(rich), jsontext.Value(logo)
	if favorite {
		v.IsFavorite = &fav
	}
	return &v, nil
}

func (a *API) loadView(ctx context.Context, id uuid.UUID) (*viewOut, error) {
	return scanView(a.db.QueryRow(ctx, `SELECT `+viewCols+` FROM issue_views v WHERE v.id = $1`, id), false)
}

// viewInitial is IssueViewSerializer(None).data: with no instance DRF
// renders each writable field's initial value.
var viewInitial = map[string]any{
	"name": "", "description": "", "filters": nil, "display_filters": nil, "display_properties": nil,
	"rich_filters": nil, "sort_order": nil, "logo_props": nil, "archived_at": nil, "deleted_at": nil,
	"created_by": nil, "updated_by": nil,
}

// viewOrNone renders a view, or the serializer's initial data for none.
func viewOrNone(v *viewOut) any {
	if v == nil {
		return viewInitial
	}
	return v
}

// viewQueryKV is the dict issue_filters builds, in insertion order.
type viewQueryKV struct {
	keys []string
	vals map[string]jsontext.Value
}

func (q *viewQueryKV) set(k string, v jsontext.Value) {
	if q.vals == nil {
		q.vals = map[string]jsontext.Value{}
	}
	if _, ok := q.vals[k]; !ok {
		q.keys = append(q.keys, k)
	}
	q.vals[k] = v
}

func (q *viewQueryKV) json() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range q.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(jsonString(k))
		b.WriteByte(':')
		b.Write(q.vals[k])
	}
	b.WriteByte('}')
	return b.String()
}

// viewIssueFilterKeys is issue_filters' ISSUE_FILTER, in order.
var viewIssueFilterKeys = []string{"state", "state_group", "estimate_point", "priority", "parent", "labels", "assignees",
	"mentions", "created_by", "logged_by", "name", "created_at", "updated_at", "start_date", "target_date", "completed_at",
	"type", "project", "cycle", "module", "intake_status", "inbox_status", "sub_issue", "subscriber", "start_target_date"}

// viewListFilters are the filters that copy a non-empty value under a
// lookup name, then (for relations) add the relation's deleted_at check.
var viewListFilters = map[string][2]string{
	"state": {"state__in", ""}, "state_group": {"state__group__in", ""}, "estimate_point": {"estimate_point__in", ""},
	"priority": {"priority__in", ""}, "parent": {"parent__in", ""},
	"labels":    {"labels__in", "label_issue__deleted_at__isnull"},
	"assignees": {"assignees__in", "issue_assignee__deleted_at__isnull"},
	"mentions":  {"issue_mention__mention__id__in", ""}, "created_by": {"created_by__in", ""},
	"logged_by": {"logged_by__in", ""}, "project": {"project__in", ""},
	"cycle":      {"issue_cycle__cycle_id__in", "issue_cycle__deleted_at__isnull"},
	"module":     {"issue_module__module_id__in", "issue_module__deleted_at__isnull"},
	"subscriber": {"issue_subscribers__subscriber_id__in", "issue_subscribers__deleted_at__isnull"},
}

// viewRelativeDate is issue_filters' pattern as re.match applies it: \d is
// any Unicode digit and $ also matches before a final newline.
var viewRelativeDate = regexp.MustCompile(`^\p{Nd}+_(weeks|months)\n?$`)

var (
	jsonTrue  = jsontext.Value("true")
	jsonFalse = jsontext.Value("false")
	jsonNull  = jsontext.Value("null")
)

// viewHasLen is whether len() works on the value (str, list or dict).
func viewHasLen(v drf.Value) bool {
	switch v.Kind() {
	case '"', '[', '{':
		return true
	}
	return false
}

// viewIssueFilters ports issue_filters(filters, "POST"), the query a view
// saves. crash is an exception issue_filters raises; dated means a relative
// date filter put a date in the query, which the JSONField can't encode,
// so saving the view raises.
func viewIssueFilters(raw jsontext.Value) (q *viewQueryKV, dated bool, crash bool) {
	q = &viewQueryKV{}
	params := drf.JSONValue(raw)
	if !drf.PyTruthy(params) {
		return q, false, false
	}
	switch params.Kind() {
	case '{':
	case '"':
		// `key in str` is a substring test, then str has no .get().
		for _, k := range viewIssueFilterKeys {
			if strings.Contains(params.Str(), k) {
				return q, false, true
			}
		}
		return q, false, false
	case '[':
		elems, _ := params.Elems()
		for _, k := range viewIssueFilterKeys {
			for _, e := range elems {
				if e.IsString() && e.Str() == k {
					return q, false, true
				}
			}
		}
		return q, false, false
	default:
		return q, false, true // `in` on a number
	}
	d := drf.DataFromJSON(raw)
	get := func(k string) drf.Value {
		if v, ok := d.Get(k); ok {
			return v
		}
		return drf.JSONValue(jsonNull)
	}
	// nonEmpty is `params.get(k) and len(params.get(k))`.
	nonEmpty := func(v drf.Value) (bool, bool) {
		if !drf.PyTruthy(v) {
			return false, true
		}
		return viewHasLen(v), viewHasLen(v)
	}
	notNullStr := func(v drf.Value) bool { return !(v.IsString() && v.Str() == "null") }
	dateFilter := func(term string, v drf.Value) bool {
		var queries []drf.Value
		switch v.Kind() {
		case '"':
			for _, r := range v.Str() {
				queries = append(queries, drf.JSONValue(jsonString(string(r))))
			}
		case '[':
			queries, _ = v.Elems()
		case '{':
			for _, k := range drf.DataFromJSON(v.Raw()).Keys() {
				queries = append(queries, drf.JSONValue(jsonString(k)))
			}
		}
		for _, qv := range queries {
			if !qv.IsString() {
				return false // no .split()
			}
			parts := strings.Split(qv.Str(), ";")
			if len(parts) < 2 {
				q.set(term+"__contains", jsonString(parts[0]))
				continue
			}
			if viewRelativeDate.MatchString(parts[0]) {
				if len(parts) == 3 {
					_, unit, _ := strings.Cut(parts[0], "_")
					if unit == "weeks" || unit == "months" {
						dated = true
					}
				}
				continue
			}
			after := false
			for _, p := range parts {
				after = after || p == "after"
			}
			if after {
				q.set(term+"__gte", jsonString(parts[0]))
			} else {
				q.set(term+"__lte", jsonString(parts[0]))
			}
		}
		return true
	}
	for _, key := range viewIssueFilterKeys {
		if !d.Has(key) {
			continue
		}
		v := get(key)
		if lf, ok := viewListFilters[key]; ok {
			set, ok := nonEmpty(v)
			if !ok {
				return q, dated, true // len() of a number
			}
			if set && notNullStr(v) {
				q.set(lf[0], v.Raw())
			}
			if lf[1] != "" {
				q.set(lf[1], jsonTrue)
			}
			continue
		}
		switch key {
		case "name":
			if !(v.IsString() && v.Str() == "") {
				q.set("name__icontains", v.Raw())
			}
		case "created_at", "updated_at", "completed_at":
			set, ok := nonEmpty(v)
			if !ok {
				return q, dated, true
			}
			if set && !dateFilter(key+"__date", v) {
				return q, dated, true
			}
		case "start_date", "target_date":
			set, ok := nonEmpty(v)
			if !ok {
				return q, dated, true
			}
			if set {
				q.set(key, v.Raw())
			}
		case "type":
			group := `["backlog","unstarted","started","completed","cancelled"]`
			if v.IsString() && v.Str() == "backlog" {
				group = `["backlog"]`
			} else if v.IsString() && v.Str() == "active" {
				group = `["unstarted","started"]`
			}
			q.set("state__group__in", jsontext.Value(group))
		case "intake_status", "inbox_status":
			set, ok := nonEmpty(v)
			if !ok {
				return q, dated, true
			}
			if set && notNullStr(v) {
				// Both read inbox_status.
				q.set("issue_intake__status__in", get("inbox_status").Raw())
			}
		case "sub_issue":
			if v.IsString() && v.Str() == "false" {
				q.set("parent__isnull", jsonTrue)
			}
		case "start_target_date":
			if v.IsString() && v.Str() == "true" {
				q.set("target_date__isnull", jsonFalse)
				q.set("start_date__isnull", jsonFalse)
			}
		}
	}
	return q, dated, false
}

// viewInput is IssueViewSerializer's validated data.
type viewInput struct {
	set     setList
	filters jsontext.Value // nil when absent
}

// validateView runs IssueViewSerializer over request.data; create requires
// name.
func (a *API) validateView(ctx context.Context, c *httpx.Ctx, data *drf.Data, create bool) (*viewInput, error) {
	v := drf.NewValidator(data, c.Loc())
	in := &viewInput{}
	set := &in.set
	if create {
		v.Require("name")
	}
	if t, ok := v.DateTime("deleted_at", true); ok {
		set.add("deleted_at", t)
	}
	if err := a.auditFields(ctx, v, set); err != nil {
		return nil, err
	}
	if s, ok := v.Char("name", drf.CharField{MaxLength: 255}); ok {
		set.add("name", *s)
	}
	if s, ok := v.Char("description", drf.CharField{AllowBlank: true}); ok {
		set.add("description", *s)
	}
	for _, f := range []string{"filters", "display_filters", "display_properties", "rich_filters"} {
		if raw, ok := v.JSON(f, false); ok {
			set.addCast(f, string(raw), "::jsonb")
			if f == "filters" {
				in.filters = raw
			}
		}
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
	}
	if raw, ok := v.JSON("logo_props", false); ok {
		set.addCast("logo_props", string(raw), "::jsonb")
	}
	if t, ok := v.DateTime("archived_at", true); ok {
		set.add("archived_at", t)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	return in, nil
}

// viewSaveQuery is IssueView.save()'s query from the view's filters.
func viewSaveQuery(filters jsontext.Value) (string, error) {
	q, dated, crash := viewIssueFilters(filters)
	if crash || dated {
		return "", errViewCrash // the filters raise, or the date can't be encoded
	}
	return q.json(), nil
}

// createView is ModelViewSet.create with perform_create: the serializer's
// create() runs issue_filters over the filters, then IssueView.save()
// computes the query again, takes the project's workspace and puts a new
// view 10000 after its siblings' highest sort_order.
func (a *API) createView(c *httpx.Ctx, projectID *uuid.UUID) error {
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	in, err := a.validateView(ctx, c, data, true)
	if err != nil {
		return err
	}
	set := &in.set
	var workspaceID uuid.UUID
	if projectID == nil {
		if err := a.db.QueryRow(ctx, `SELECT id FROM workspaces WHERE slug = $1 AND deleted_at IS NULL`,
			c.Param("slug")).Scan(&workspaceID); err != nil {
			return err
		}
	}
	filters := in.filters
	if filters == nil {
		filters = jsontext.Value("{}")
	}
	q, dated, crash := viewIssueFilters(filters)
	if crash {
		return errViewCrash
	}
	var maxOrder *float64
	if projectID != nil {
		// self.project goes through the base manager: any project row.
		if err := a.db.QueryRow(ctx, `SELECT workspace_id FROM projects WHERE id = $1`, *projectID).Scan(&workspaceID); err != nil {
			return err
		}
		err = a.db.QueryRow(ctx, `SELECT max(sort_order) FROM issue_views WHERE project_id = $1 AND deleted_at IS NULL`,
			*projectID).Scan(&maxOrder)
	} else {
		err = a.db.QueryRow(ctx, `SELECT max(sort_order) FROM issue_views
			WHERE workspace_id = $1 AND project_id IS NULL AND deleted_at IS NULL`, workspaceID).Scan(&maxOrder)
	}
	if err != nil {
		return err
	}
	if dated {
		return errViewCrash // the INSERT can't encode the date
	}
	if maxOrder != nil {
		set.drop("sort_order")
		set.add("sort_order", *maxOrder+10000)
	}
	set.drop("created_by_id") // save() sets created_by to the requester
	set.addCast("query", q.json(), "::jsonb")
	set.add("workspace_id", workspaceID)
	set.add("project_id", projectID)
	set.add("owned_by_id", c.User.ID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, set.insertSQL("issue_views"), set.args...).Scan(&id); err != nil {
		return err
	}
	v, err := a.loadView(ctx, id)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, v)
}

// updateView is the partial_update both viewsets share: the locked and
// owner checks, then IssueViewSerializer's update(), whose save() derives
// the query from the view's filters.
func (a *API) updateView(c *httpx.Ctx, projectID *uuid.UUID) error {
	ctx := c.Context()
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	var (
		locked  bool
		owner   uuid.UUID
		filters string
	)
	err = a.db.QueryRow(ctx, `SELECT v.is_locked, v.owned_by_id, v.filters::text FROM issue_views v
		JOIN workspaces w ON w.id = v.workspace_id
		WHERE v.id = $1 AND w.slug = $2 AND ($3::uuid IS NULL OR v.project_id = $3) AND v.deleted_at IS NULL`,
		pk, c.Param("slug"), projectID).Scan(&locked, &owner, &filters)
	if err != nil {
		return err
	}
	if locked {
		return httpx.Err(http.StatusBadRequest, "view is locked")
	}
	if owner != c.User.ID {
		return httpx.Err(http.StatusBadRequest, "Only the owner of the view can update the view")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	in, err := a.validateView(ctx, c, data, false)
	if err != nil {
		return err
	}
	if in.filters != nil {
		if _, _, crash := viewIssueFilters(in.filters); crash {
			return errViewCrash
		}
	} else {
		in.filters = jsontext.Value(filters)
	}
	query, err := viewSaveQuery(in.filters)
	if err != nil {
		return err
	}
	set := &in.set
	set.addCast("query", query, "::jsonb")
	set.add("updated_by_id", c.User.ID)
	if _, err := a.db.Exec(ctx, `UPDATE issue_views SET `+set.sql()+`, updated_at = now() WHERE id = $1`,
		append([]any{pk}, set.args...)...); err != nil {
		return err
	}
	v, err := a.loadView(ctx, pk)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, v)
}

// allowViewCreator is allow_permission(roles, creator=True, model=IssueView)
// at project or workspace level: any active workspace member who created
// the view, else the role check.
func (a *API) allowViewCreator(workspace bool, roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		pk, err := c.UUIDParam("pk")
		if err != nil {
			return err
		}
		if !workspace {
			if _, err := c.UUIDParam("project_id"); err != nil {
				return err
			}
		}
		ctx := c.Context()
		role, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
		if err != nil {
			return err
		}
		if role == 0 {
			return errNoRole
		}
		var creator bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM issue_views
			WHERE id = $1 AND created_by_id = $2 AND deleted_at IS NULL)`, pk, c.User.ID).Scan(&creator); err != nil {
			return err
		}
		if creator {
			return h(c)
		}
		if workspace {
			return a.allowWorkspace(roles, h)(c)
		}
		return a.allowProject(roles, h)(c)
	}
}

// viewPK resolves <uuid:pk> before any permission check, as the URL
// resolver does.
func viewPK(h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		if _, err := c.UUIDParam("pk"); err != nil {
			return err
		}
		return h(c)
	}
}

// projectViewFrom is IssueViewViewSet.get_queryset: the project's views the
// user owns or that are public, through an active membership of the
// unarchived project, with is_favorite.
const projectViewFrom = `, EXISTS (SELECT 1 FROM user_favorites f JOIN workspaces fw ON fw.id = f.workspace_id
		WHERE f.user_id = $3 AND f.entity_identifier = v.id AND f.entity_type = 'view' AND f.project_id = $2
			AND fw.slug = $1 AND f.deleted_at IS NULL) AS is_favorite
	FROM issue_views v JOIN workspaces w ON w.id = v.workspace_id JOIN projects p ON p.id = v.project_id
	WHERE v.deleted_at IS NULL AND w.slug = $1 AND v.project_id = $2
		AND EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.member_id = $3 AND pm.is_active)
		AND p.archived_at IS NULL AND (v.owned_by_id = $3 OR v.access = 1)`

// projectGuestLimited is whether the user is an active guest of a project
// that doesn't share everything with guests.
func (a *API) projectGuestLimited(ctx context.Context, slug string, projectID, user uuid.UUID) (bool, error) {
	var viewAll bool
	if err := a.db.QueryRow(ctx, `SELECT guest_view_all_features FROM projects WHERE id = $1 AND deleted_at IS NULL`,
		projectID).Scan(&viewAll); err != nil {
		return false, err
	}
	var guest bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE w.slug = $1 AND pm.project_id = $2 AND pm.member_id = $3 AND pm.role = 5 AND pm.is_active
			AND pm.deleted_at IS NULL)`, slug, projectID, user).Scan(&guest)
	return guest && !viewAll, err
}

// listProjectViews ports IssueViewViewSet.list: favorites first, then by
// name; a limited guest sees only their own views. ?fields= is ignored
// (DynamicBaseSerializer drops it).
func (a *API) listProjectViews(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	limited, err := a.projectGuestLimited(ctx, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	sql := `SELECT ` + viewCols + projectViewFrom
	if limited {
		sql += ` AND v.owned_by_id = $3`
	}
	rows, err := a.db.Query(ctx, sql+` ORDER BY is_favorite DESC, v.name ASC`, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	views, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*viewOut, error) { return scanView(row, true) })
	if err != nil {
		return err
	}
	if views == nil {
		views = []*viewOut{}
	}
	return c.JSON(http.StatusOK, views)
}

// getProjectView ports IssueViewViewSet.retrieve. A view it can't see
// renders as the serializer's initial data (a limited guest crashes on it
// instead), and the visit is recorded either way.
func (a *API) getProjectView(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	v, err := scanView(a.db.QueryRow(ctx, `SELECT `+viewCols+projectViewFrom+` AND v.id = $4`, slug, projectID, c.User.ID, pk), true)
	if errors.Is(err, pgx.ErrNoRows) {
		v, err = nil, nil
	}
	if err != nil {
		return err
	}
	limited, err := a.projectGuestLimited(ctx, slug, projectID, c.User.ID)
	if err != nil {
		return err
	}
	if limited {
		if v == nil {
			return errViewCrash // None.owned_by
		}
		if v.OwnedBy != c.User.ID {
			return httpx.Err(http.StatusForbidden, "You are not allowed to view this issue")
		}
	}
	a.recordVisit(ctx, slug, "view", pk, c.User.ID, &projectID)
	return c.JSON(http.StatusOK, viewOrNone(v))
}

func (a *API) createProjectView(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	return a.createView(c, &projectID)
}

func (a *API) updateProjectView(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	return a.updateView(c, &projectID)
}

// deleteProjectView ports IssueViewViewSet.destroy: a project admin or the
// owner soft-deletes the view, and everyone's favorites of it and visits to
// it (hard-deleted) go too.
func (a *API) deleteProjectView(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var owner uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT v.owned_by_id FROM issue_views v JOIN workspaces w ON w.id = v.workspace_id
		WHERE v.id = $1 AND v.project_id = $2 AND w.slug = $3 AND v.deleted_at IS NULL`, pk, projectID, slug).Scan(&owner); err != nil {
		return err
	}
	var admin bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM project_members pm JOIN workspaces w ON w.id = pm.workspace_id
		WHERE w.slug = $1 AND pm.project_id = $2 AND pm.member_id = $3 AND pm.role = 20 AND pm.is_active
			AND pm.deleted_at IS NULL)`, slug, projectID, c.User.ID).Scan(&admin); err != nil {
		return err
	}
	if !admin && owner != c.User.ID {
		return httpx.Err(http.StatusBadRequest, "Only admin or owner can delete the view")
	}
	if err := softdelete.Row(ctx, a.db, "issue_views", pk, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE user_favorites f SET deleted_at = now() FROM workspaces w
		WHERE w.id = f.workspace_id AND f.project_id = $1 AND w.slug = $2 AND f.entity_identifier = $3
			AND f.entity_type = 'view' AND f.deleted_at IS NULL`, projectID, slug, pk); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `DELETE FROM user_recent_visits r USING workspaces w
		WHERE w.id = r.workspace_id AND r.project_id = $1 AND w.slug = $2 AND r.entity_identifier = $3
			AND r.entity_name = 'view' AND r.deleted_at IS NULL`, projectID, slug, pk); err != nil {
		return err
	}
	return c.NoContent()
}

// workspaceViewFrom is WorkspaceViewViewSet.get_queryset: the workspace's
// own views (no project) the user owns or that are public.
const workspaceViewFrom = ` FROM issue_views v JOIN workspaces w ON w.id = v.workspace_id
	WHERE v.deleted_at IS NULL AND w.slug = $1 AND v.project_id IS NULL AND (v.owned_by_id = $2 OR v.access = 1)`

// viewOrderAllow is VIEW_ORDER_BY_ALLOWLIST.
var viewOrderAllow = []string{"created_at", "updated_at", "name"}

// listWorkspaceViews ports WorkspaceViewViewSet.list: ordered by
// ?order_by=; a workspace guest sees only their own views.
func (a *API) listWorkspaceViews(c *httpx.Ctx) error {
	ctx := c.Context()
	slug := c.Param("slug")
	role, err := a.workspaceRole(ctx, slug, c.User.ID)
	if err != nil {
		return err
	}
	field, desc := sanitizeOrderBy(c.Query("order_by"), viewOrderAllow, "-created_at")
	dir := " ASC"
	if desc {
		dir = " DESC"
	}
	sql := `SELECT ` + viewCols + workspaceViewFrom
	if role == roleGuest {
		sql += ` AND v.owned_by_id = $2`
	}
	rows, err := a.db.Query(ctx, sql+` ORDER BY v.`+field+dir, slug, c.User.ID)
	if err != nil {
		return err
	}
	views, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*viewOut, error) { return scanView(row, false) })
	if err != nil {
		return err
	}
	if views == nil {
		views = []*viewOut{}
	}
	return c.JSON(http.StatusOK, views)
}

// getWorkspaceView ports WorkspaceViewViewSet.retrieve, which has no role
// check: any signed-in user reads a visible view (or the initial data) and
// gets a visit recorded.
func (a *API) getWorkspaceView(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	v, err := scanView(a.db.QueryRow(ctx, `SELECT `+viewCols+workspaceViewFrom+` AND v.id = $3`, slug, c.User.ID, pk), false)
	if errors.Is(err, pgx.ErrNoRows) {
		v, err = nil, nil
	}
	if err != nil {
		return err
	}
	a.recordVisit(ctx, slug, "view", pk, c.User.ID, nil)
	return c.JSON(http.StatusOK, viewOrNone(v))
}

func (a *API) createWorkspaceView(c *httpx.Ctx) error { return a.createView(c, nil) }

func (a *API) updateWorkspaceView(c *httpx.Ctx) error { return a.updateView(c, nil) }

// deleteWorkspaceView ports WorkspaceViewViewSet.destroy: any view of the
// workspace (project views too), by a workspace admin or its owner; only
// workspace-level favorites of it are removed.
func (a *API) deleteWorkspaceView(c *httpx.Ctx) error {
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	var owner uuid.UUID
	if err := a.db.QueryRow(ctx, `SELECT v.owned_by_id FROM issue_views v JOIN workspaces w ON w.id = v.workspace_id
		WHERE v.id = $1 AND w.slug = $2 AND v.deleted_at IS NULL`, pk, slug).Scan(&owner); err != nil {
		return err
	}
	role, err := a.workspaceRole(ctx, slug, c.User.ID)
	if err != nil {
		return err
	}
	if role != roleAdmin && owner != c.User.ID {
		return httpx.Err(http.StatusBadRequest, "Only admin or owner can delete the view")
	}
	if err := softdelete.Row(ctx, a.db, "issue_views", pk, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE user_favorites f SET deleted_at = now() FROM workspaces w
		WHERE w.id = f.workspace_id AND w.slug = $1 AND f.entity_identifier = $2 AND f.project_id IS NULL
			AND f.entity_type = 'view' AND f.deleted_at IS NULL`, slug, pk); err != nil {
		return err
	}
	return c.NoContent()
}
