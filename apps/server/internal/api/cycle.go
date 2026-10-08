package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
	"plane-lite/server/internal/softdelete"
)

// The cycle views of plane/app/views/cycle/base.py (CycleViewSet,
// CycleDateCheckEndpoint, CycleUserPropertiesEndpoint) and
// workspace/cycle.py (WorkspaceCyclesEndpoint).

// cycleTime is a cycle date out of user_timezone_converter: a .values()
// datetime moved into the project's timezone, which DRF's encoder prints
// with that offset.
type cycleTime struct {
	t   time.Time
	loc *time.Location
}

func (t cycleTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + httpx.FormatDateTime(t.t, t.loc) + `"`), nil
}

func cycleTimeIn(t *time.Time, loc *time.Location) *cycleTime {
	if t == nil {
		return nil
	}
	return &cycleTime{*t, loc}
}

// cycleValues is one dict of CycleViewSet's .values() reads. The list adds
// cancelled_issues and retrieve adds sub_issues.
type cycleValues struct {
	ID               uuid.UUID      `json:"id"`
	WorkspaceID      uuid.UUID      `json:"workspace_id"`
	ProjectID        uuid.UUID      `json:"project_id"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	StartDate        *cycleTime     `json:"start_date"`
	EndDate          *cycleTime     `json:"end_date"`
	OwnedByID        uuid.UUID      `json:"owned_by_id"`
	ViewProps        jsontext.Value `json:"view_props"`
	SortOrder        float64        `json:"sort_order"`
	ExternalSource   *string        `json:"external_source"`
	ExternalID       *string        `json:"external_id"`
	ProgressSnapshot jsontext.Value `json:"progress_snapshot"`
	LogoProps        jsontext.Value `json:"logo_props"`
	IsFavorite       bool           `json:"is_favorite"`
	TotalIssues      int            `json:"total_issues"`
	CancelledIssues  *int           `json:"cancelled_issues,omitzero"`
	CompletedIssues  int            `json:"completed_issues"`
	AssigneeIDs      []uuid.UUID    `json:"assignee_ids"`
	Status           string         `json:"status"`
	Version          int            `json:"version"`
	CreatedBy        *uuid.UUID     `json:"created_by"`
	SubIssues        *int           `json:"sub_issues,omitzero"`
}

// cycleCountFilter is the filter of CycleViewSet's issue counts, over the
// cycle_issues ci, issues i and states st joins.
const cycleCountFilter = `ci.deleted_at IS NULL AND i.archived_at IS NULL AND i.deleted_at IS NULL AND NOT i.is_draft`

// cycleStatusSQL is the status annotation against the instant $4.
const cycleStatusSQL = `CASE WHEN c.start_date <= $4 AND c.end_date >= $4 THEN 'CURRENT'
	WHEN c.start_date > $4 THEN 'UPCOMING' WHEN c.end_date < $4 THEN 'COMPLETED' ELSE 'DRAFT' END`

// cycleFavoriteSQL is the is_favorite annotation for user $1 in project $3
// of workspace $2.
const cycleFavoriteSQL = `EXISTS (SELECT 1 FROM user_favorites f JOIN workspaces fw ON fw.id = f.workspace_id
	WHERE f.deleted_at IS NULL AND f.entity_identifier = c.id AND f.entity_type = 'cycle' AND f.project_id = $3
		AND f.user_id = $1 AND fw.slug = $2)`

// cycleQuery is CycleViewSet.get_queryset read through .values(): the user
// ($1) must be an active member of the project ($3) of workspace $2, and
// statuses are computed at $4. extra adds conditions (arguments from $5);
// withCancelled and withSub add the list's and retrieve's extra values.
func cycleQuery(extra string, withCancelled, withSub bool) string {
	var b strings.Builder
	b.WriteString(`SELECT c.id, c.workspace_id, c.project_id, c.name, c.description, c.start_date, c.end_date,
		c.owned_by_id, c.view_props, c.sort_order, c.external_source, c.external_id, c.progress_snapshot, c.logo_props,
		` + cycleFavoriteSQL + `,
		COUNT(DISTINCT ci.issue_id) FILTER (WHERE ` + cycleCountFilter + `),
		COUNT(DISTINCT ci.issue_id) FILTER (WHERE ` + cycleCountFilter + ` AND st."group" = 'completed'),`)
	if withCancelled {
		b.WriteString(`COUNT(DISTINCT ci.issue_id) FILTER (WHERE ` + cycleCountFilter + ` AND st."group" = 'cancelled'),`)
	} else {
		b.WriteString(`NULL::bigint,`)
	}
	b.WriteString(`COALESCE(array_agg(DISTINCT ia.assignee_id) FILTER (WHERE ia.assignee_id IS NOT NULL
			AND ia.deleted_at IS NULL), '{}'),
		` + cycleStatusSQL + `, c.version, c.created_by_id, `)
	if withSub {
		// Not correlated: the cycle is the one asked for ($5).
		b.WriteString(`(SELECT count(*) FROM issues si LEFT JOIN states sst ON sst.id = si.state_id
			JOIN projects sp ON sp.id = si.project_id JOIN cycle_issues sci ON sci.issue_id = si.id
			WHERE si.deleted_at IS NULL AND sst."group" IS DISTINCT FROM 'triage' AND si.archived_at IS NULL
				AND sp.archived_at IS NULL AND NOT si.is_draft AND sci.cycle_id = $5 AND sci.deleted_at IS NULL
				AND si.parent_id IS NOT NULL AND si.project_id = $3)`)
	} else {
		b.WriteString(`NULL::bigint`)
	}
	b.WriteString(`
	FROM cycles c JOIN workspaces w ON w.id = c.workspace_id JOIN projects p ON p.id = c.project_id
		JOIN project_members pm ON pm.project_id = p.id
		LEFT JOIN cycle_issues ci ON ci.cycle_id = c.id LEFT JOIN issues i ON i.id = ci.issue_id
		LEFT JOIN states st ON st.id = i.state_id LEFT JOIN issue_assignees ia ON ia.issue_id = i.id
	WHERE c.deleted_at IS NULL AND w.slug = $2 AND c.project_id = $3 AND pm.is_active AND pm.member_id = $1
		AND p.archived_at IS NULL` + extra + `
	GROUP BY c.id
	ORDER BY 15 DESC, c.created_at DESC`)
	return b.String()
}

// queryCycles runs a cycleQuery, rendering the dates in the project's
// timezone.
func (a *API) queryCycles(ctx context.Context, loc *time.Location, sql string, args ...any) ([]*cycleValues, error) {
	rows, err := a.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (*cycleValues, error) {
		var (
			v                cycleValues
			start, end       *time.Time
			total, completed int64
			cancelled, sub   *int64
			ver              int32
		)
		err := row.Scan(&v.ID, &v.WorkspaceID, &v.ProjectID, &v.Name, &v.Description, &start, &end, &v.OwnedByID,
			&v.ViewProps, &v.SortOrder, &v.ExternalSource, &v.ExternalID, &v.ProgressSnapshot, &v.LogoProps,
			&v.IsFavorite, &total, &completed, &cancelled, &v.AssigneeIDs, &v.Status, &ver, &v.CreatedBy, &sub)
		v.StartDate, v.EndDate = cycleTimeIn(start, loc), cycleTimeIn(end, loc)
		v.TotalIssues, v.CompletedIssues, v.Version = int(total), int(completed), int(ver)
		v.CancelledIssues, v.SubIssues = intOf(cancelled), intOf(sub)
		return &v, err
	})
}

// cycleProjectTZ is Project.objects.get(id=project_id).timezone.
func (a *API) cycleProjectTZ(ctx context.Context, projectID uuid.UUID) (*time.Location, error) {
	var name string
	if err := a.db.QueryRow(ctx, `SELECT timezone FROM projects WHERE id = $1 AND deleted_at IS NULL`, projectID).
		Scan(&name); err != nil {
		return nil, err
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, errViewCrash // pytz.UnknownTimeZoneError
	}
	return loc, nil
}

// listCycles ports CycleViewSet.list. cycle_view=current keeps the running
// cycles; when there are none the list that follows is built from the same
// filtered queryset, so it is empty too.
func (a *API) listCycles(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	loc, err := a.cycleProjectTZ(ctx, projectID)
	if err != nil {
		return err
	}
	extra := ` AND c.archived_at IS NULL`
	if c.Query("cycle_view") == "current" {
		extra += ` AND c.start_date <= $4 AND c.end_date >= $4`
	}
	cycles, err := a.queryCycles(ctx, loc, cycleQuery(extra, true, false), c.User.ID, c.Param("slug"), projectID, time.Now())
	if err != nil {
		return err
	}
	if cycles == nil {
		cycles = []*cycleValues{}
	}
	return c.JSON(http.StatusOK, cycles)
}

// loadCycleValues is the create and update views' read back: one cycle of
// get_queryset() through .values() (no cancelled_issues), or nil.
func (a *API) loadCycleValues(ctx context.Context, c *httpx.Ctx, projectID, id uuid.UUID, loc *time.Location) (*cycleValues, error) {
	cycles, err := a.queryCycles(ctx, loc, cycleQuery(` AND c.id = $5`, false, false),
		c.User.ID, c.Param("slug"), projectID, time.Now(), id)
	if err != nil || len(cycles) == 0 {
		return nil, err
	}
	return cycles[0], nil
}

// getCycle ports CycleViewSet.retrieve.
func (a *API) getCycle(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	loc, err := a.cycleProjectTZ(ctx, projectID)
	if err != nil {
		return err
	}
	slug := c.Param("slug")
	cycles, err := a.queryCycles(ctx, loc, cycleQuery(` AND c.id = $5 AND c.archived_at IS NULL`, false, true),
		c.User.ID, slug, projectID, time.Now(), pk)
	if err != nil {
		return err
	}
	if len(cycles) == 0 {
		return httpx.Err(http.StatusNotFound, "Cycle not found")
	}
	a.recordVisit(ctx, slug, "cycle", pk, c.User.ID, &projectID)
	return c.JSON(http.StatusOK, cycles[0])
}

// cycleWrite is CycleWriteSerializer's validated data.
type cycleWrite struct {
	set        setList
	start, end *time.Time
	hasStart   bool
	hasEnd     bool
}

// validateCycle runs CycleWriteSerializer ("__all__" less workspace,
// project, owned_by and archived_at) over request.data, then its validate():
// start before end, and with both dates, each moved to the project's
// midnight (convert_to_utc).
func (a *API) validateCycle(ctx context.Context, c *httpx.Ctx, data *drf.Data, projectID uuid.UUID, partial bool) (*cycleWrite, error) {
	v := drf.NewValidator(data, c.Loc())
	w := &cycleWrite{}
	set := &w.set
	if !partial {
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
	if t, ok := v.DateTime("start_date", true); ok {
		w.start, w.hasStart = t, true
	}
	if t, ok := v.DateTime("end_date", true); ok {
		w.end, w.hasEnd = t, true
	}
	for _, f := range []string{"view_props", "progress_snapshot", "logo_props"} {
		if raw, ok := v.JSON(f, false); ok {
			set.addCast(f, string(raw), "::jsonb")
		}
	}
	if f, ok := v.Float("sort_order"); ok {
		set.add("sort_order", f)
	}
	for _, f := range []string{"external_source", "external_id"} {
		if s, ok := v.Char(f, drf.CharField{MaxLength: 255, AllowNull: true, AllowBlank: true}); ok {
			set.add(f, s)
		}
	}
	if s, ok := v.Choice("timezone", userTimezones, drf.ChoiceField{}); ok {
		set.add("timezone", *s)
	}
	if n, ok := v.Int("version", -2147483648, 2147483647); ok {
		set.add("version", n)
	}
	if err := v.Err(); err != nil {
		return nil, err
	}
	if w.start != nil && w.end != nil {
		if w.start.After(*w.end) {
			return nil, httpx.Body(http.StatusBadRequest, map[string][]string{"non_field_errors": {"Start date cannot exceed end date"}})
		}
		// The project comes from request.data's project_id if truthy.
		tzProject := projectID
		if raw, ok := data.Get("project_id"); ok && drf.PyTruthy(raw) {
			id, ok := drf.UUIDValue(raw)
			if !ok {
				return nil, httpx.Body(http.StatusBadRequest, map[string][]string{
					"non_field_errors": {"“" + drf.PyStr(raw) + "” is not a valid UUID."}})
			}
			tzProject = id
		}
		loc, err := a.cycleProjectTZ(ctx, tzProject)
		if err != nil {
			return nil, err
		}
		userLoc := c.Loc()
		start, end := cycleDayStart(w.start.In(userLoc), loc), cycleDayEnd(w.end.In(userLoc), loc)
		w.start, w.end = &start, &end
	}
	if w.hasStart {
		set.add("start_date", w.start)
	}
	if w.hasEnd {
		set.add("end_date", w.end)
	}
	return w, nil
}

// cycleDayStart is convert_to_utc(date, is_start_date=True): one second
// past the date's midnight in the project's timezone, or now when that
// date is the project's today. pytz adds the second to the localized
// midnight, keeping midnight's offset.
func cycleDayStart(d time.Time, loc *time.Location) time.Time {
	now := time.Now()
	y, m, day := d.Date()
	ny, nm, nd := now.In(loc).Date()
	if y == ny && m == nm && day == nd {
		return now.UTC().Truncate(time.Microsecond)
	}
	return time.Date(y, m, day, 0, 0, 0, 0, loc).Add(time.Second).UTC()
}

// cycleDayEnd is convert_to_utc(date): midnight in the project's timezone
// plus 23:59 (at midnight's offset, as pytz adds it).
func cycleDayEnd(d time.Time, loc *time.Location) time.Time {
	y, m, day := d.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, loc).Add(23*time.Hour + 59*time.Minute).UTC()
}

// createCycle ports CycleViewSet.create: both dates or neither.
func (a *API) createCycle(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash // request.data.get on a list
	}
	isNone := func(name string) bool {
		v, ok := data.Get(name)
		return !ok || v.IsNull()
	}
	if isNone("start_date") != isNone("end_date") {
		return httpx.Err(http.StatusBadRequest, "Both start date and end date are either required or are to be null")
	}
	w, err := a.validateCycle(ctx, c, data, projectID, false)
	if err != nil {
		return err
	}
	set := &w.set
	set.drop("created_by_id") // save() records the requester
	var (
		workspaceID uuid.UUID
		minSort     *float64
	)
	if err := a.db.QueryRow(ctx, `SELECT p.workspace_id, (SELECT min(x.sort_order) FROM cycles x
			WHERE x.project_id = p.id AND x.deleted_at IS NULL)
		FROM projects p WHERE p.id = $1`, projectID).Scan(&workspaceID, &minSort); err != nil {
		return err
	}
	if minSort != nil {
		set.drop("sort_order")
		set.add("sort_order", *minSort-10000)
	}
	if !slices.Contains(set.cols, "description") {
		set.add("description", "")
	}
	set.add("project_id", projectID)
	set.add("workspace_id", workspaceID)
	set.add("owned_by_id", c.User.ID)
	set.add("created_by_id", c.User.ID)
	var id uuid.UUID
	if err := a.db.QueryRow(ctx, set.insertSQL("cycles"), set.args...).Scan(&id); err != nil {
		return err
	}
	loc, err := a.cycleProjectTZ(ctx, projectID)
	if err != nil {
		return err
	}
	out, err := a.loadCycleValues(ctx, c, projectID, id, loc)
	if err != nil {
		return err
	}
	if out == nil {
		return errViewCrash // user_timezone_converter(None)
	}
	return c.JSON(http.StatusCreated, out)
}

// cycleRow is the columns the write views read from a cycle.
type cycleRow struct {
	ID          uuid.UUID
	WorkspaceID uuid.UUID
	ProjectID   uuid.UUID
	Name        string
	Start, End  *time.Time
	ArchivedAt  *time.Time
	TotalIssues int // annotated by some views
	Snapshot    jsontext.Value
}

// loadCycle is Cycle.objects.get(workspace__slug=slug, project_id=..., pk=...).
func (a *API) loadCycle(ctx context.Context, slug string, projectID, pk uuid.UUID) (*cycleRow, error) {
	var r cycleRow
	err := a.db.QueryRow(ctx, `SELECT c.id, c.workspace_id, c.project_id, c.name, c.start_date, c.end_date, c.archived_at,
			c.progress_snapshot
		FROM cycles c JOIN workspaces w ON w.id = c.workspace_id
		WHERE c.id = $1 AND c.project_id = $2 AND w.slug = $3 AND c.deleted_at IS NULL`, pk, projectID, slug).
		Scan(&r.ID, &r.WorkspaceID, &r.ProjectID, &r.Name, &r.Start, &r.End, &r.ArchivedAt, &r.Snapshot)
	return &r, err
}

// updateCycle ports CycleViewSet.partial_update. A completed cycle only
// takes a body with sort_order, but then the whole body is applied.
func (a *API) updateCycle(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	ctx := c.Context()
	loc, err := a.cycleProjectTZ(ctx, projectID)
	if err != nil {
		return err
	}
	var (
		archivedAt, end *time.Time
	)
	err = a.db.QueryRow(ctx, `SELECT c.archived_at, c.end_date FROM cycles c JOIN workspaces w ON w.id = c.workspace_id
		JOIN projects p ON p.id = c.project_id
		WHERE c.id = $1 AND c.project_id = $2 AND w.slug = $3 AND c.deleted_at IS NULL AND p.archived_at IS NULL
			AND EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.member_id = $4 AND pm.is_active)`,
		pk, projectID, c.Param("slug"), c.User.ID).Scan(&archivedAt, &end)
	if errors.Is(err, pgx.ErrNoRows) {
		return errViewCrash // None.archived_at
	}
	if err != nil {
		return err
	}
	if archivedAt != nil {
		return httpx.Err(http.StatusBadRequest, "Archived cycle cannot be updated")
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if end != nil && end.Before(time.Now()) && !cycleDataHas(data, "sort_order") {
		return httpx.Err(http.StatusBadRequest, "The Cycle has already been completed so it cannot be edited")
	}
	w, err := a.validateCycle(ctx, c, data, projectID, true)
	if err != nil {
		return err
	}
	set := &w.set
	if _, err := a.db.Exec(ctx, `UPDATE cycles SET `+set.sql()+cycleSetSep(set)+`updated_at = now(), updated_by_id = $`+
		strconv.Itoa(len(set.args)+2)+` WHERE id = $1`, append(append([]any{pk}, set.args...), c.User.ID)...); err != nil {
		return err
	}
	out, err := a.loadCycleValues(ctx, c, projectID, pk, loc)
	if err != nil {
		return err
	}
	if out == nil {
		return errViewCrash
	}
	return c.JSON(http.StatusOK, out)
}

// cycleSetSep joins a SET list to the columns that follow it.
func cycleSetSep(set *setList) string {
	if len(set.cols) == 0 {
		return ""
	}
	return ", "
}

// cycleDataHas is `name in request.data`: a key of a dict, an item of a
// list.
func cycleDataHas(data *drf.Data, name string) bool {
	if data.IsDict() {
		_, ok := data.Get(name)
		return ok
	}
	elems, _ := data.Root().Elems()
	for _, e := range elems {
		if e.IsString() && e.Str() == name {
			return true
		}
	}
	return false
}

// allowCycleCreator is allow_permission(roles, creator=True, model=Cycle):
// an active workspace member who created the cycle passes whatever their
// project role.
func (a *API) allowCycleCreator(roles []int, h httpx.HandlerFunc) httpx.HandlerFunc {
	return func(c *httpx.Ctx) error {
		ctx := c.Context()
		wsRole, err := a.workspaceRole(ctx, c.Param("slug"), c.User.ID)
		if err != nil {
			return err
		}
		if wsRole == 0 {
			return errNoRole
		}
		pk, err := c.UUIDParam("pk")
		if err != nil {
			return err
		}
		var creator bool
		if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cycles
			WHERE id = $1 AND created_by_id = $2 AND deleted_at IS NULL)`, pk, c.User.ID).Scan(&creator); err != nil {
			return err
		}
		if creator {
			return h(c)
		}
		return a.allowProject(roles, h)(c)
	}
}

// deleteCycle ports CycleViewSet.destroy: one cycle.activity.deleted for
// all its issues (sent with the cycle's id as issue_id), the soft delete,
// the requester's favorite and everyone's recent visits.
func (a *API) deleteCycle(c *httpx.Ctx) error {
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
	cy, err := a.loadCycle(ctx, slug, projectID, pk)
	if err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT issue_id::text FROM cycle_issues WHERE cycle_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC`, pk)
	if err != nil {
		return err
	}
	issues, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if issues == nil {
		issues = []string{}
	}
	requested, err := cycleJSON(map[string]any{"cycle_id": pk.String(), "cycle_name": cy.Name, "issues": issues})
	if err != nil {
		return err
	}
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "cycle.activity.deleted", RequestedData: &requested, IssueID: pk.String(),
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
	if err := softdelete.Row(ctx, a.db, "cycles", pk, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE user_favorites SET deleted_at = now()
		WHERE user_id = $1 AND entity_type = 'cycle' AND entity_identifier = $2 AND project_id = $3
			AND deleted_at IS NULL`, c.User.ID, pk, projectID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `DELETE FROM user_recent_visits v USING workspaces w
		WHERE w.id = v.workspace_id AND v.project_id = $1 AND w.slug = $2 AND v.entity_identifier = $3
			AND v.entity_name = 'cycle' AND v.deleted_at IS NULL`, projectID, slug, pk); err != nil {
		return err
	}
	return c.NoContent()
}

// cycleJSON is json.dumps of a task argument.
func cycleJSON(v any) (string, error) {
	b, err := json.Marshal(v, json.Deterministic(true))
	return string(b), err
}

// cycleDateCheck ports CycleDateCheckEndpoint.post: whether a live cycle of
// the project overlaps the dates (other than cycle_id).
func (a *API) cycleDateCheck(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	startV, sok := data.Get("start_date")
	endV, eok := data.Get("end_date")
	if !sok || !eok || !drf.PyTruthy(startV) || !drf.PyTruthy(endV) {
		return httpx.Err(http.StatusBadRequest, "Start date and end date both are required")
	}
	loc, err := a.cycleProjectTZ(ctx, projectID)
	if err != nil {
		return err
	}
	parse := func(v drf.Value) (time.Time, bool) { return drf.StrptimeYMD(drf.PyStr(v)) }
	sd, ok := parse(startV)
	if !ok {
		return errViewCrash // strptime raises ValueError
	}
	ed, ok := parse(endV)
	if !ok {
		return errViewCrash
	}
	start, end := cycleDayStart(sd, loc), cycleDayEnd(ed, loc)
	var exclude *uuid.UUID
	if v, ok := data.Get("cycle_id"); ok && !v.IsNull() {
		id, ok := drf.UUIDValue(v)
		if !ok {
			return errFilterDetail
		}
		exclude = &id
	}
	var exists bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cycles c JOIN workspaces w ON w.id = c.workspace_id
		WHERE c.deleted_at IS NULL AND w.slug = $1 AND c.project_id = $2
			AND ((c.start_date <= $3 AND c.end_date >= $3) OR (c.start_date <= $4 AND c.end_date >= $4)
				OR (c.start_date >= $3 AND c.end_date <= $4))
			AND ($5::uuid IS NULL OR c.id <> $5))`, c.Param("slug"), projectID, start, end, exclude).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return c.JSON(http.StatusOK, map[string]any{
			"error":  "You have a cycle already on the given dates, if you want to create a draft cycle you can do that by removing dates",
			"status": false,
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"status": true})
}

// cycleUserProperties is CycleUserPropertiesSerializer ("__all__").
type cycleUserProperties struct {
	ID                uuid.UUID      `json:"id"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	DeletedAt         *time.Time     `json:"deleted_at"`
	CreatedBy         *uuid.UUID     `json:"created_by"`
	UpdatedBy         *uuid.UUID     `json:"updated_by"`
	Filters           jsontext.Value `json:"filters"`
	DisplayFilters    jsontext.Value `json:"display_filters"`
	DisplayProperties jsontext.Value `json:"display_properties"`
	RichFilters       jsontext.Value `json:"rich_filters"`
	Project           uuid.UUID      `json:"project"`
	Workspace         uuid.UUID      `json:"workspace"`
	Cycle             uuid.UUID      `json:"cycle"`
	User              uuid.UUID      `json:"user"`
}

// findCycleUserProperties is CycleUserProperties.objects.get(user, cycle,
// project, workspace__slug).
func (a *API) findCycleUserProperties(ctx context.Context, slug string, projectID, cycleID, user uuid.UUID) (*cycleUserProperties, error) {
	var p cycleUserProperties
	err := a.db.QueryRow(ctx, `SELECT x.id, x.created_at, x.updated_at, x.deleted_at, x.created_by_id, x.updated_by_id,
			x.filters, x.display_filters, x.display_properties, x.rich_filters, x.project_id, x.workspace_id, x.cycle_id, x.user_id
		FROM cycle_user_properties x JOIN workspaces w ON w.id = x.workspace_id
		WHERE x.user_id = $1 AND x.cycle_id = $2 AND x.project_id = $3 AND w.slug = $4 AND x.deleted_at IS NULL`,
		user, cycleID, projectID, slug).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt, &p.DeletedAt, &p.CreatedBy, &p.UpdatedBy,
		&p.Filters, &p.DisplayFilters, &p.DisplayProperties, &p.RichFilters, &p.Project, &p.Workspace, &p.Cycle, &p.User)
	return &p, err
}

// getCycleUserProperties ports CycleUserPropertiesEndpoint.get, which
// creates the row on first read (whether or not the cycle is live).
func (a *API) getCycleUserProperties(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	cycleID, err := c.UUIDParam("cycle_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	p, err := a.findCycleUserProperties(ctx, slug, projectID, cycleID, c.User.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := a.db.Exec(ctx, `INSERT INTO cycle_user_properties (user_id, project_id, cycle_id, workspace_id,
				created_by_id)
			SELECT $1, p.id, $3, p.workspace_id, $1 FROM projects p WHERE p.id = $2`, c.User.ID, projectID, cycleID); err != nil {
			return err
		}
		p, err = a.findCycleUserProperties(ctx, slug, projectID, cycleID, c.User.ID)
	}
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, p)
}

// patchCycleUserProperties ports CycleUserPropertiesEndpoint.patch: each
// of the four filters present in the body replaces the stored one (null
// fails the NOT NULL column). It answers 201.
func (a *API) patchCycleUserProperties(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	cycleID, err := c.UUIDParam("cycle_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	p, err := a.findCycleUserProperties(ctx, slug, projectID, cycleID, c.User.ID)
	if err != nil {
		return err
	}
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	var set setList
	for _, f := range []string{"filters", "rich_filters", "display_filters", "display_properties"} {
		if v, ok := data.Get(f); ok {
			if v.IsNull() {
				set.add(f, nil)
			} else {
				set.addCast(f, string(v.Raw()), "::jsonb")
			}
		}
	}
	if _, err := a.db.Exec(ctx, `UPDATE cycle_user_properties SET `+set.sql()+cycleSetSep(&set)+
		`updated_at = now(), updated_by_id = $`+strconv.Itoa(len(set.args)+2)+` WHERE id = $1`,
		append(append([]any{p.ID}, set.args...), c.User.ID)...); err != nil {
		return err
	}
	p, err = a.findCycleUserProperties(ctx, slug, projectID, cycleID, c.User.ID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, p)
}

// workspaceCycle is CycleSerializer over WorkspaceCyclesEndpoint's
// queryset: is_favorite and status are not annotated there, so the
// serializer leaves them out.
type workspaceCycle struct {
	ID               uuid.UUID      `json:"id"`
	WorkspaceID      uuid.UUID      `json:"workspace_id"`
	ProjectID        uuid.UUID      `json:"project_id"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	StartDate        *time.Time     `json:"start_date"`
	EndDate          *time.Time     `json:"end_date"`
	OwnedByID        uuid.UUID      `json:"owned_by_id"`
	ViewProps        jsontext.Value `json:"view_props"`
	SortOrder        float64        `json:"sort_order"`
	ExternalSource   *string        `json:"external_source"`
	ExternalID       *string        `json:"external_id"`
	ProgressSnapshot jsontext.Value `json:"progress_snapshot"`
	LogoProps        jsontext.Value `json:"logo_props"`
	TotalIssues      int            `json:"total_issues"`
	CancelledIssues  int            `json:"cancelled_issues"`
	CompletedIssues  int            `json:"completed_issues"`
	StartedIssues    int            `json:"started_issues"`
	UnstartedIssues  int            `json:"unstarted_issues"`
	BacklogIssues    int            `json:"backlog_issues"`
}

// workspaceCycles ports WorkspaceCyclesEndpoint.get: the live cycles of
// the user's projects. The counts are not distinct.
func (a *API) workspaceCycles(c *httpx.Ctx) error {
	ctx := c.Context()
	const live = `ci.deleted_at IS NULL AND i.archived_at IS NULL AND i.deleted_at IS NULL AND NOT i.is_draft`
	group := func(g string) string {
		return `COUNT(st."group") FILTER (WHERE ` + live + ` AND st."group" = '` + g + `')`
	}
	rows, err := a.db.Query(ctx, `SELECT c.id, c.workspace_id, c.project_id, c.name, c.description, c.start_date,
			c.end_date, c.owned_by_id, c.view_props, c.sort_order, c.external_source, c.external_id, c.progress_snapshot,
			c.logo_props, COUNT(ci.id) FILTER (WHERE `+live+`), `+group("cancelled")+`, `+group("completed")+`,
			`+group("started")+`, `+group("unstarted")+`, `+group("backlog")+`
		FROM cycles c JOIN projects p ON p.id = c.project_id JOIN project_members pm ON pm.project_id = p.id
			JOIN workspaces w ON w.id = c.workspace_id
			LEFT JOIN cycle_issues ci ON ci.cycle_id = c.id LEFT JOIN issues i ON i.id = ci.issue_id
			LEFT JOIN states st ON st.id = i.state_id
		WHERE c.deleted_at IS NULL AND p.archived_at IS NULL AND pm.is_active AND pm.member_id = $1 AND w.slug = $2
			AND c.archived_at IS NULL
		GROUP BY c.id
		ORDER BY c.created_at DESC`, c.User.ID, c.Param("slug"))
	if err != nil {
		return err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*workspaceCycle, error) {
		var w workspaceCycle
		var n [6]int64
		err := row.Scan(&w.ID, &w.WorkspaceID, &w.ProjectID, &w.Name, &w.Description, &w.StartDate, &w.EndDate,
			&w.OwnedByID, &w.ViewProps, &w.SortOrder, &w.ExternalSource, &w.ExternalID, &w.ProgressSnapshot,
			&w.LogoProps, &n[0], &n[1], &n[2], &n[3], &n[4], &n[5])
		w.TotalIssues, w.CancelledIssues, w.CompletedIssues = int(n[0]), int(n[1]), int(n[2])
		w.StartedIssues, w.UnstartedIssues, w.BacklogIssues = int(n[3]), int(n[4]), int(n[5])
		return &w, err
	})
	if err != nil {
		return err
	}
	if out == nil {
		out = []*workspaceCycle{}
	}
	return c.JSON(http.StatusOK, out)
}
