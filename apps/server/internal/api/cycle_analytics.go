package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// Cycle progress and analytics: CycleProgressEndpoint and
// CycleAnalyticsEndpoint (cycle/base.py), the distributions they share with
// the archive detail and transfer_cycle_issues, and
// plane.utils.analytics_plot.burndown_plot for cycles.

// cycleIssuesFrom is Issue.issue_objects filtered to the live issues of
// cycle $1 in project $2 of workspace $3, with the joins the
// distributions add.
const cycleIssuesFrom = ` FROM issues i LEFT JOIN states s ON s.id = i.state_id JOIN projects p ON p.id = i.project_id
	JOIN cycle_issues ci ON ci.issue_id = i.id JOIN workspaces w ON w.id = i.workspace_id`

const cycleIssuesWhere = ` WHERE ` + issueBaseWhere + ` AND ci.cycle_id = $1 AND ci.deleted_at IS NULL
	AND i.project_id = $2 AND w.slug = $3`

// cycleAvatarSQL is the distributions' avatar_url annotation.
const cycleAvatarSQL = `CASE WHEN u.avatar_asset_id IS NOT NULL THEN '/api/assets/v2/static/' || u.avatar_asset_id::text || '/'
	WHEN u.avatar_asset_id IS NULL THEN u.avatar ELSE NULL END`

// cycleDist selects one distribution's variant.
type cycleDist struct {
	labels bool // by label rather than assignee
	points bool // estimate sums rather than issue counts
	// count is the counted column: the analytics view counts the grouped
	// relation's id (so issues without one count 0), the others i.id.
	count string
	// names adds first_name and last_name and orders by them (the archive
	// detail's assignees).
	names bool
}

// cycleDistribution runs a distribution: Issue.issue_objects in the cycle,
// grouped by assignee or label (through every relation row, removed ones
// included), with issue counts or estimate sums.
func (a *API) cycleDistribution(ctx context.Context, slug string, projectID, cycleID uuid.UUID, d cycleDist) ([]*issueRow, error) {
	var cols []valueCol
	from := cycleIssuesFrom
	order := ""
	if d.labels {
		from += ` LEFT JOIN issue_labels il ON il.issue_id = i.id LEFT JOIN labels l ON l.id = il.label_id`
		cols = []valueCol{{"label_name", "l.name"}, {"color", "l.color"}, {"label_id", "il.label_id"}}
		order = "1"
	} else {
		from += ` LEFT JOIN issue_assignees ia ON ia.issue_id = i.id LEFT JOIN users u ON u.id = ia.assignee_id`
		if d.names {
			cols = []valueCol{{"first_name", "u.first_name"}, {"last_name", "u.last_name"}, {"assignee_id", "ia.assignee_id"},
				{"avatar_url", cycleAvatarSQL}, {"display_name", "u.display_name"}}
			order = "1, 2"
		} else {
			cols = []valueCol{{"display_name", "u.display_name"}, {"assignee_id", "ia.assignee_id"}, {"avatar_url", cycleAvatarSQL}}
			order = "1"
		}
	}
	groups := make([]string, len(cols))
	for i := range cols {
		groups[i] = cols[i].expr
	}
	const (
		live      = `i.archived_at IS NULL AND NOT i.is_draft`
		completed = `i.archived_at IS NULL AND i.completed_at IS NOT NULL AND NOT i.is_draft`
		pending   = `i.archived_at IS NULL AND i.completed_at IS NULL AND NOT i.is_draft`
	)
	if d.points {
		from += ` LEFT JOIN estimate_points ep ON ep.id = i.estimate_point_id`
		v := `SUM(ep.value::double precision)`
		cols = append(cols, valueCol{"total_estimates", v}, valueCol{"completed_estimates", v + ` FILTER (WHERE ` + completed + `)`},
			valueCol{"pending_estimates", v + ` FILTER (WHERE ` + pending + `)`})
	} else {
		n := `COUNT(` + d.count + `)`
		cols = append(cols, valueCol{"total_issues", n + ` FILTER (WHERE ` + live + `)`},
			valueCol{"completed_issues", n + ` FILTER (WHERE ` + completed + `)`},
			valueCol{"pending_issues", n + ` FILTER (WHERE ` + pending + `)`})
	}
	sql := `SELECT ` + colsSQL(cols) + from + cycleIssuesWhere + ` GROUP BY ` + strings.Join(groups, ", ") +
		` ORDER BY ` + order
	rows, err := a.db.Query(ctx, sql, cycleID, projectID, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*issueRow{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := &issueRow{fields: map[string]any{}}
		for i, c := range cols {
			r.set(c.name, cycleJSONValue(vals[i]))
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// cycleJSONValue converts a scanned column as DRF renders it.
func cycleJSONValue(v any) any {
	switch x := v.(type) {
	case [16]byte:
		return uuid.UUID(x)
	case int64:
		return int(x)
	case int32:
		return int(x)
	}
	return v
}

// cyclePointsProject is the views' estimate_type: the project uses a points
// estimate.
func (a *API) cyclePointsProject(ctx context.Context, slug string, projectID uuid.UUID) (bool, error) {
	var ok bool
	err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects p JOIN estimates e ON e.id = p.estimate_id
		JOIN workspaces w ON w.id = p.workspace_id
		WHERE p.deleted_at IS NULL AND p.estimate_id IS NOT NULL AND e.type = 'points' AND p.id = $1 AND w.slug = $2)`,
		projectID, slug).Scan(&ok)
	return ok, err
}

// cyclePyFloat is Python's float() of an estimate value; a value it
// rejects raises ValueError.
func cyclePyFloat(s string) (float64, error) {
	raw, _ := json.Marshal(s)
	f, ok := drf.PyFloat(drf.JSONValue(raw))
	if !ok {
		return 0, errViewCrash
	}
	return f, nil
}

// cycleBurndown is burndown_plot for a cycle: from the start date to the
// end date (as UTC dates), the issues (or points) still pending at the end
// of each day, completed_at being read in the user's timezone; days after
// today (UTC) are null. total is the cycle's annotated total_issues.
func (a *API) cycleBurndown(ctx context.Context, c *httpx.Ctx, projectID, cycleID uuid.UUID, start, end *time.Time, total int,
	points bool) (map[string]any, error) {
	slug := c.Param("slug")
	tz := c.Loc().String()
	chart := map[string]any{}
	var totalPoints float64
	if points {
		rows, err := a.db.Query(ctx, `SELECT ep.value`+cycleIssuesFrom+` JOIN estimate_points ep ON ep.id = i.estimate_point_id`+
			cycleIssuesWhere+` AND i.estimate_point_id IS NOT NULL ORDER BY i.created_at DESC`, cycleID, projectID, slug)
		if err != nil {
			return nil, err
		}
		values, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, v := range values {
			f, err := cyclePyFloat(v)
			if err != nil {
				return nil, err
			}
			totalPoints += f
		}
	}
	if start == nil || end == nil {
		return chart, nil
	}
	type done struct {
		date  *time.Time
		count int64
		value string
	}
	var sql string
	if points {
		sql = `SELECT (i.completed_at AT TIME ZONE $4)::date, 0::bigint, ep.value` + cycleIssuesFrom +
			` JOIN estimate_points ep ON ep.id = i.estimate_point_id` + cycleIssuesWhere +
			` AND i.estimate_point_id IS NOT NULL ORDER BY 1`
	} else {
		sql = `SELECT (i.completed_at AT TIME ZONE $4)::date, count(i.id), ''` + cycleIssuesFrom + cycleIssuesWhere +
			` GROUP BY 1 ORDER BY 1`
	}
	rows, err := a.db.Query(ctx, sql, cycleID, projectID, slug, tz)
	if err != nil {
		return nil, err
	}
	completions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (done, error) {
		var d done
		err := row.Scan(&d.date, &d.count, &d.value)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	first := time.Date(start.UTC().Year(), start.UTC().Month(), start.UTC().Day(), 0, 0, 0, 0, time.UTC)
	last := time.Date(end.UTC().Year(), end.UTC().Month(), end.UTC().Day(), 0, 0, 0, 0, time.UTC)
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		key := day.Format(time.DateOnly)
		var doneIssues int64
		var donePoints float64
		for _, d := range completions {
			if d.date == nil || d.date.After(day) {
				continue
			}
			if points {
				f, err := cyclePyFloat(d.value)
				if err != nil {
					return nil, err
				}
				donePoints += f
			} else {
				doneIssues += d.count
			}
		}
		switch {
		case day.After(today):
			chart[key] = nil
		case points:
			chart[key] = totalPoints - donePoints
		default:
			chart[key] = total - int(doneIssues)
		}
	}
	return chart, nil
}

// cycleSnapshot is a truthy progress_snapshot read as a dict; ok is false
// for an empty one. Anything but a dict has no .get, a 500.
func cycleSnapshot(raw jsontext.Value) (*drf.Data, bool, error) {
	if !drf.PyTruthy(drf.JSONValue(raw)) {
		return nil, false, nil
	}
	if raw.Kind() != '{' {
		return nil, false, errViewCrash
	}
	return drf.DataFromJSON(raw), true, nil
}

// snapshotGet is dict.get(key, def) on a snapshot.
func snapshotGet(d *drf.Data, key string, def any) any {
	if v, ok := d.Get(key); ok {
		return v.Raw()
	}
	return def
}

// cycleProgress ports CycleProgressEndpoint.get: estimate sums per state
// group, and issue counts (from the snapshot once issues were transferred).
func (a *API) cycleProgress(c *httpx.Ctx) error {
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
	cy, err := a.loadCycle(ctx, slug, projectID, cycleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusNotFound, "Cycle not found")
	}
	if err != nil {
		return err
	}
	sum := func(group string) string {
		return `SUM(CASE WHEN s."group" = '` + group + `' THEN ep.value::double precision ELSE 0 END)`
	}
	var est [5]*float64
	var totalEst float64
	if err := a.db.QueryRow(ctx, `SELECT `+sum("backlog")+`, `+sum("unstarted")+`, `+sum("started")+`, `+sum("cancelled")+`,
			`+sum("completed")+`, COALESCE(SUM(ep.value::double precision), 0)`+cycleIssuesFrom+`
			JOIN estimate_points ep ON ep.id = i.estimate_point_id JOIN estimates e ON e.id = ep.estimate_id`+
		cycleIssuesWhere+` AND e.type = 'points'`, cycleID, projectID, slug).
		Scan(&est[0], &est[1], &est[2], &est[3], &est[4], &totalEst); err != nil {
		return err
	}
	orZero := func(f *float64) any {
		if f == nil || *f == 0 {
			return 0
		}
		return *f
	}
	counts := map[string]any{}
	snap, ok, err := cycleSnapshot(cy.Snapshot)
	if err != nil {
		return err
	}
	groups := []string{"backlog", "unstarted", "started", "cancelled", "completed"}
	if ok {
		for _, g := range append(groups, "total") {
			counts[g] = snapshotGet(snap, g+"_issues", 0)
		}
	} else {
		count := func(extra string, args ...any) (int64, error) {
			var n int64
			err := a.db.QueryRow(ctx, `SELECT count(*)`+cycleIssuesFrom+cycleIssuesWhere+extra,
				append([]any{cycleID, projectID, slug}, args...)...).Scan(&n)
			return n, err
		}
		for _, g := range groups {
			n, err := count(` AND s."group" = $4`, g)
			if err != nil {
				return err
			}
			counts[g] = int(n)
		}
		n, err := count("")
		if err != nil {
			return err
		}
		counts["total"] = int(n)
	}
	return c.JSON(http.StatusOK, map[string]any{
		"backlog_estimate_points":   orZero(est[0]),
		"unstarted_estimate_points": orZero(est[1]),
		"started_estimate_points":   orZero(est[2]),
		"cancelled_estimate_points": orZero(est[3]),
		"completed_estimate_points": orZero(est[4]),
		"total_estimate_points":     totalEst,
		"backlog_issues":            counts["backlog"],
		"total_issues":              counts["total"],
		"completed_issues":          counts["completed"],
		"cancelled_issues":          counts["cancelled"],
		"started_issues":            counts["started"],
		"unstarted_issues":          counts["unstarted"],
	})
}

// cycleAnalytics ports CycleAnalyticsEndpoint.get: the assignee and label
// distributions and the burndown, of issues (?type=issues, the default) or
// of points (?type=points, for a points-estimated project); a cycle whose
// issues were transferred answers from its snapshot.
func (a *API) cycleAnalytics(c *httpx.Ctx) error {
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
	kind := "issues"
	if c.HasQuery("type") {
		kind = c.Query("type")
	}
	cy, err := a.loadCycle(ctx, slug, projectID, cycleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errViewCrash // None.start_date
	}
	if err != nil {
		return err
	}
	if cy.Start == nil || cy.End == nil {
		return httpx.Err(http.StatusBadRequest, "Cycle has no start or end date")
	}
	snap, ok, err := cycleSnapshot(cy.Snapshot)
	if err != nil {
		return err
	}
	if ok {
		dist, has := snap.Get("distribution")
		if !has {
			dist = drf.JSONValue(jsontext.Value("{}"))
		}
		if dist.Kind() != '{' {
			return errViewCrash
		}
		d := drf.DataFromJSON(dist.Raw())
		return c.JSON(http.StatusOK, map[string]any{
			"labels":           snapshotGet(d, "labels", []any{}),
			"assignees":        snapshotGet(d, "assignees", []any{}),
			"completion_chart": snapshotGet(d, "completion_chart", map[string]any{}),
		})
	}
	var total int64
	if err := a.db.QueryRow(ctx, `SELECT COUNT(DISTINCT ci.issue_id) FILTER (WHERE `+cycleCountFilter+`)
		FROM cycles c LEFT JOIN cycle_issues ci ON ci.cycle_id = c.id LEFT JOIN issues i ON i.id = ci.issue_id
		WHERE c.id = $1`, cycleID).Scan(&total); err != nil {
		return err
	}
	pointsProject, err := a.cyclePointsProject(ctx, slug, projectID)
	if err != nil {
		return err
	}
	var assignees, labels any = []any{}, []any{}
	var chart any = map[string]any{}
	if (kind == "points" && pointsProject) || kind == "issues" {
		points := kind == "points"
		as, err := a.cycleDistribution(ctx, slug, projectID, cycleID, cycleDist{points: points, count: "ia.assignee_id"})
		if err != nil {
			return err
		}
		ls, err := a.cycleDistribution(ctx, slug, projectID, cycleID, cycleDist{labels: true, points: points, count: "il.label_id"})
		if err != nil {
			return err
		}
		ch, err := a.cycleBurndown(ctx, c, projectID, cycleID, cy.Start, cy.End, int(total), points)
		if err != nil {
			return err
		}
		assignees, labels, chart = as, ls, ch
	}
	return c.JSON(http.StatusOK, map[string]any{"assignees": assignees, "labels": labels, "completion_chart": chart})
}
