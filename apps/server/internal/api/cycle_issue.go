package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// The cycle issue views: CycleIssueViewSet (cycle/issue.py) and
// TransferCycleIssueEndpoint with plane.utils.cycle_transfer_issues.

// listCycleIssues ports CycleIssueViewSet.list: the issue list of
// IssueViewSet over the cycle's live issues, with the list endpoint's 0
// (not NULL) counts, and without a queryset for issue_group_values (so
// grouping by a date or the creator crashes, as in the archive list).
func (a *API) listCycleIssues(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	cycleID, err := c.UUIDParam("cycle_id")
	if err != nil {
		return err
	}
	l := &issueList{a: a, c: c, slug: c.Param("slug"), projectID: projectID, from: issueFrom, archived: true,
		plainCounts: true, q: &issueQuery{tz: c.Loc().String()}}
	q := l.q
	q.filter(issueBaseWhere)
	sc := newScope()
	q.filter(q.render(lookup{rel: "cycle_issues", col: "cycle_id", cond: q.eq(cycleID, "::uuid")}, sc, false, false) +
		" AND " + q.render(lookup{rel: "cycle_issues", col: "deleted_at", cond: isNull, isnull: true}, sc, false, false))
	q.filter("i.project_id = " + q.arg(projectID))
	q.filter("w.slug = " + q.arg(l.slug))
	if err := l.applyFilters(false); err != nil {
		return err
	}
	l.filtered = l.q.clone()
	l.prepare(c.Query("order_by"), c.Query("group_by"), c.Query("sub_group_by"))
	return l.paginate(c.Context())
}

// cycleIssueIDs is the `issues` value as issue_id__in and set() see it:
// the list's items (a dict's keys), each a UUID the lookup accepts, keyed
// by the raw item for the set difference.
func cycleIssueIDs(v drf.Value) ([]string, []uuid.UUID, error) {
	var items []drf.Value
	switch v.Kind() {
	case '[':
		items, _ = v.Elems()
	case '"':
		return nil, nil, errFilterDetail // each character fails UUIDField.to_python
	default:
		return nil, nil, errViewCrash // not iterable
	}
	var raw []string
	var ids []uuid.UUID
	for _, it := range items {
		id, ok := drf.UUIDValue(it)
		if !ok {
			return nil, nil, errFilterDetail
		}
		raw = append(raw, drf.PyStr(it))
		ids = append(ids, id)
	}
	return raw, ids, nil
}

// addCycleIssues ports CycleIssueViewSet.create: issues in another cycle of
// the project move here, the others get a new CycleIssue (an issue already
// in this cycle fails the unique index), and one cycle.activity.created
// covers both.
func (a *API) addCycleIssues(c *httpx.Ctx) error {
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
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	issuesV, ok := data.Get("issues")
	if !ok || !drf.PyTruthy(issuesV) {
		return httpx.Err(http.StatusBadRequest, "Issues are required")
	}
	cy, err := a.loadCycle(ctx, slug, projectID, cycleID)
	if err != nil {
		return err
	}
	if cy.End != nil && cy.End.Before(time.Now()) {
		return httpx.Err(http.StatusBadRequest, "The Cycle has already been completed so no new issues can be added")
	}
	raw, ids, err := cycleIssueIDs(issuesV)
	if err != nil {
		return err
	}
	type moved struct {
		id, issue, oldCycle uuid.UUID
	}
	rows, err := a.db.Query(ctx, `SELECT ci.id, ci.issue_id, ci.cycle_id FROM cycle_issues ci
		JOIN workspaces w ON w.id = ci.workspace_id
		WHERE ci.deleted_at IS NULL AND NOT (ci.cycle_id = $1) AND ci.issue_id = ANY($2) AND w.slug = $3
			AND ci.project_id = $4
		ORDER BY ci.created_at DESC`, cycleID, ids, slug, projectID)
	if err != nil {
		return err
	}
	existing, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (moved, error) {
		var m moved
		err := row.Scan(&m.id, &m.issue, &m.oldCycle)
		return m, err
	})
	if err != nil {
		return err
	}
	taken := map[string]bool{}
	for _, m := range existing {
		taken[m.issue.String()] = true
	}
	var fresh []uuid.UUID
	for i, r := range raw {
		if !taken[r] {
			fresh = append(fresh, ids[i])
		}
	}
	rows, err = a.db.Query(ctx, `SELECT i.id FROM issues i LEFT JOIN states s ON s.id = i.state_id
		JOIN projects p ON p.id = i.project_id JOIN workspaces w ON w.id = i.workspace_id
		WHERE `+issueBaseWhere+` AND w.slug = $1 AND i.project_id = $2 AND i.id = ANY($3)
		ORDER BY i.created_at DESC`, slug, projectID, fresh)
	if err != nil {
		return err
	}
	newIssues, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return err
	}
	type createdRecord struct {
		Model  string         `json:"model"`
		PK     string         `json:"pk"`
		Fields map[string]any `json:"fields"`
	}
	created := []createdRecord{}
	if len(newIssues) > 0 {
		// bulk_create: one statement, each row stamped as it is built.
		rows, err := a.db.Query(ctx, `INSERT INTO cycle_issues (created_at, updated_at, project_id, workspace_id,
				created_by_id, updated_by_id, cycle_id, issue_id)
			SELECT clock_timestamp(), clock_timestamp(), $1, $2, $3, $3, $4, x.issue
			FROM unnest($5::uuid[]) WITH ORDINALITY AS x(issue, n) ORDER BY x.n
			RETURNING id, issue_id`, projectID, cy.WorkspaceID, c.User.ID, cycleID, newIssues)
		if err != nil {
			return err
		}
		recs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (createdRecord, error) {
			var id, issue uuid.UUID
			err := row.Scan(&id, &issue)
			return createdRecord{Model: "db.cycleissue", PK: id.String(), Fields: map[string]any{
				"cycle": cycleID.String(), "issue": issue.String(), "project": projectID.String(),
				"workspace": cy.WorkspaceID.String(),
			}}, err
		})
		if err != nil {
			return err
		}
		created = recs
	}
	updated := []map[string]any{}
	if len(existing) > 0 {
		movedIDs := make([]uuid.UUID, len(existing))
		for i, m := range existing {
			movedIDs[i] = m.id
			updated = append(updated, map[string]any{
				"old_cycle_id": m.oldCycle.String(), "new_cycle_id": cycleID.String(), "issue_id": m.issue.String(),
			})
		}
		if _, err := a.db.Exec(ctx, `UPDATE cycle_issues SET cycle_id = $1 WHERE id = ANY($2) AND deleted_at IS NULL`,
			cycleID, movedIDs); err != nil {
			return err
		}
	}
	requested, err := cycleJSON(map[string]any{"cycles_list": issuesV.Raw()})
	if err != nil {
		return err
	}
	createdJSON, err := cycleJSON(created)
	if err != nil {
		return err
	}
	current, err := cycleJSON(map[string]any{"updated_cycle_issues": updated, "created_cycle_issues": createdJSON})
	if err != nil {
		return err
	}
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "cycle.activity.created", RequestedData: &requested, CurrentInstance: &current,
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
	return c.JSON(http.StatusCreated, map[string]any{"message": "success"})
}

// removeCycleIssue ports CycleIssueViewSet.destroy: the activity is sent
// whether or not the issue is in the cycle, then the rows are soft-deleted
// (a queryset delete).
func (a *API) removeCycleIssue(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	cycleID, err := c.UUIDParam("cycle_id")
	if err != nil {
		return err
	}
	issueID, err := c.UUIDParam("issue_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	requested, err := cycleJSON(map[string]any{"cycle_id": cycleID.String(), "issues": []string{issueID.String()}})
	if err != nil {
		return err
	}
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "cycle.activity.deleted", RequestedData: &requested, IssueID: issueID.String(),
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
	if _, err := a.db.Exec(ctx, `UPDATE cycle_issues ci SET deleted_at = now() FROM workspaces w
		WHERE w.id = ci.workspace_id AND ci.issue_id = $1 AND w.slug = $2 AND ci.project_id = $3 AND ci.cycle_id = $4
			AND ci.deleted_at IS NULL`, issueID, c.Param("slug"), projectID, cycleID); err != nil {
		return err
	}
	return c.NoContent()
}

// transferCycleIssues ports TransferCycleIssueEndpoint.post and
// transfer_cycle_issues: the source cycle's progress (counts,
// distributions, burndowns) is saved in its progress_snapshot, then its
// unfinished issues move to the new cycle. The cycle.activity.created it
// sends passes created_cycle_issues as a list, which the task cannot
// json.loads, so no activity is written.
func (a *API) transferCycleIssues(c *httpx.Ctx) error {
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
	data, err := drf.Parse(c.R)
	if err != nil {
		return err
	}
	if !data.IsDict() {
		return errViewCrash
	}
	newV, ok := data.Get("new_cycle_id")
	if !ok || !drf.PyTruthy(newV) {
		return httpx.Err(http.StatusBadRequest, "New Cycle Id is required")
	}
	newID, ok := drf.UUIDValue(newV)
	if !ok {
		return errFilterDetail
	}
	target, err := a.loadCycle(ctx, slug, projectID, newID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errViewCrash // None.end_date
	}
	if err != nil {
		return err
	}
	if target.End != nil && target.End.Before(time.Now()) {
		return httpx.Err(http.StatusBadRequest, "The cycle where the issues are transferred is already completed")
	}
	old, err := a.loadCycle(ctx, slug, projectID, cycleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.Err(http.StatusBadRequest, "Source cycle not found")
	}
	if err != nil {
		return err
	}
	const live = `ci.deleted_at IS NULL AND i.archived_at IS NULL AND i.deleted_at IS NULL AND NOT i.is_draft`
	group := func(g string) string {
		return `COUNT(st."group") FILTER (WHERE ` + live + ` AND st."group" = '` + g + `')`
	}
	var n [6]int64
	if err := a.db.QueryRow(ctx, `SELECT COUNT(ci.id) FILTER (WHERE `+live+`), `+group("completed")+`, `+group("cancelled")+`,
			`+group("started")+`, `+group("unstarted")+`, `+group("backlog")+`
		FROM cycles c LEFT JOIN cycle_issues ci ON ci.cycle_id = c.id LEFT JOIN issues i ON i.id = ci.issue_id
			LEFT JOIN states st ON st.id = i.state_id
		WHERE c.id = $1`, cycleID).Scan(&n[0], &n[1], &n[2], &n[3], &n[4], &n[5]); err != nil {
		return err
	}
	total := int(n[0])
	pointsProject, err := a.cyclePointsProject(ctx, slug, projectID)
	if err != nil {
		return err
	}
	estimate := map[string]any{}
	if pointsProject {
		as, err := a.cycleDistribution(ctx, slug, projectID, cycleID, cycleDist{points: true})
		if err != nil {
			return err
		}
		ls, err := a.cycleDistribution(ctx, slug, projectID, cycleID, cycleDist{labels: true, points: true})
		if err != nil {
			return err
		}
		chart, err := a.cycleBurndown(ctx, c, projectID, cycleID, old.Start, old.End, total, true)
		if err != nil {
			return err
		}
		estimate = map[string]any{"labels": ls, "assignees": as, "completion_chart": chart}
	}
	as, err := a.cycleDistribution(ctx, slug, projectID, cycleID, cycleDist{count: "i.id"})
	if err != nil {
		return err
	}
	ls, err := a.cycleDistribution(ctx, slug, projectID, cycleID, cycleDist{labels: true, count: "i.id"})
	if err != nil {
		return err
	}
	chart, err := a.cycleBurndown(ctx, c, projectID, cycleID, old.Start, old.End, total, false)
	if err != nil {
		return err
	}
	snapshot, err := cycleJSON(map[string]any{
		"total_issues": total, "completed_issues": n[1], "cancelled_issues": n[2], "started_issues": n[3],
		"unstarted_issues": n[4], "backlog_issues": n[5],
		"distribution":          map[string]any{"labels": ls, "assignees": as, "completion_chart": chart},
		"estimate_distribution": estimate,
	})
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE cycles SET progress_snapshot = $2::jsonb WHERE id = $1`, cycleID, snapshot); err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, `SELECT ci.id, ci.issue_id FROM cycle_issues ci JOIN issues i ON i.id = ci.issue_id
		JOIN states st ON st.id = i.state_id JOIN workspaces w ON w.id = ci.workspace_id
		WHERE ci.deleted_at IS NULL AND ci.cycle_id = $1 AND i.archived_at IS NULL AND NOT i.is_draft
			AND st."group" IN ('backlog', 'unstarted', 'started') AND ci.project_id = $2 AND w.slug = $3
		ORDER BY ci.created_at DESC`, cycleID, projectID, slug)
	if err != nil {
		return err
	}
	type moving struct{ id, issue uuid.UUID }
	moves, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (moving, error) {
		var m moving
		err := row.Scan(&m.id, &m.issue)
		return m, err
	})
	if err != nil {
		return err
	}
	updated := []map[string]any{}
	if len(moves) > 0 {
		ids := make([]uuid.UUID, len(moves))
		for i, m := range moves {
			ids[i] = m.id
			updated = append(updated, map[string]any{
				"old_cycle_id": cycleID.String(), "new_cycle_id": newID.String(), "issue_id": m.issue.String(),
			})
		}
		if _, err := a.db.Exec(ctx, `UPDATE cycle_issues SET cycle_id = $1 WHERE id = ANY($2) AND deleted_at IS NULL`,
			newID, ids); err != nil {
			return err
		}
	}
	requested := `{"cycles_list": []}`
	current, err := cycleJSON(map[string]any{"updated_cycle_issues": updated, "created_cycle_issues": []any{}})
	if err != nil {
		return err
	}
	a.enqueueIssueActivity(ctx, issueActivityJob{
		Type: "cycle.activity.created", RequestedData: &requested, CurrentInstance: &current,
		ActorID: c.User.ID.String(), ProjectID: projectID.String(), Epoch: time.Now().Unix(),
		Subscriber: true, Notification: true,
	})
	return c.JSON(http.StatusOK, map[string]any{"message": "Success"})
}
