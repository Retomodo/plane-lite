package api

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/drf"
	"plane-lite/server/internal/httpx"
)

// The module retrieve views' extras: ModuleDetailSerializer data plus the
// assignee and label distributions and the burndown charts.

// moduleDetailResponse is a retrieve view's body: ModuleDetailSerializer
// data plus the distributions.
type moduleDetailResponse struct {
	*moduleDetail        `json:",inline"`
	EstimateDistribution map[string]any `json:"estimate_distribution"`
	Distribution         map[string]any `json:"distribution"`
}

// moduleDetailBody builds a retrieve view's body. m is nil when the
// archive view's queryset.first() found nothing; archived selects that
// view's variants.
func (a *API) moduleDetailBody(ctx context.Context, c *httpx.Ctx, slug string, projectID, pk uuid.UUID, m *moduleRow, archived bool) (any, error) {
	var detail *moduleDetail
	if m != nil {
		links, err := a.moduleLinksOf(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		var subIssues int
		if err := a.db.QueryRow(ctx, `SELECT count(si.id) FROM issues si JOIN module_issues smi ON smi.issue_id = si.id
			WHERE `+issueObjects("si")+` AND smi.deleted_at IS NULL AND smi.module_id = $1 AND si.parent_id IS NOT NULL
				AND si.project_id = $2`, pk, projectID).Scan(&subIssues); err != nil {
			return nil, err
		}
		detail = &moduleDetail{moduleSerialized: m.serialized(), LinkModule: links, SubIssues: subIssues,
			BacklogEstimatePoints: m.Estimates[0], UnstartedEstimatePoints: m.Estimates[1],
			StartedEstimatePoints: m.Estimates[2], CancelledEstimatePoints: m.Estimates[3]}
	}
	var points bool
	if err := a.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM projects p JOIN estimates e ON e.id = p.estimate_id
		JOIN workspaces w ON w.id = p.workspace_id
		WHERE p.deleted_at IS NULL AND e.type = 'points' AND p.id = $1 AND w.slug = $2)`, projectID, slug).Scan(&points); err != nil {
		return nil, err
	}
	dist := moduleDistQuery{slug: slug, projectID: projectID, moduleID: pk, tz: c.Loc().String()}
	estimate := map[string]any{}
	if points {
		assignees, err := a.moduleAssigneeDistribution(ctx, dist, true)
		if err != nil {
			return nil, err
		}
		// The archive view's estimate label distribution forgets the
		// issue_module__deleted_at filter.
		labels, err := a.moduleLabelDistribution(ctx, dist, true, !archived)
		if err != nil {
			return nil, err
		}
		estimate["assignees"], estimate["labels"] = assignees, labels
		if m != nil && m.StartDate != nil && m.TargetDate != nil {
			chart, err := a.moduleBurndown(ctx, dist, m, true)
			if err != nil {
				return nil, err
			}
			estimate["completion_chart"] = chart
		}
	}
	assignees, err := a.moduleAssigneeDistribution(ctx, dist, false)
	if err != nil {
		return nil, err
	}
	labels, err := a.moduleLabelDistribution(ctx, dist, false, true)
	if err != nil {
		return nil, err
	}
	distribution := map[string]any{"assignees": assignees, "labels": labels, "completion_chart": map[string]any{}}
	// retrieve() also wants a non-empty module; the archive view does not.
	if m != nil && m.StartDate != nil && m.TargetDate != nil && (archived || m.Issues[5] > 0) {
		chart, err := a.moduleBurndown(ctx, dist, m, false)
		if err != nil {
			return nil, err
		}
		distribution["completion_chart"] = chart
	}
	if detail == nil {
		// ModuleDetailSerializer(None).data: the initial value of its one
		// writable field.
		return map[string]any{"member_ids": []any{}, "estimate_distribution": estimate, "distribution": distribution}, nil
	}
	return moduleDetailResponse{moduleDetail: detail, EstimateDistribution: estimate, Distribution: distribution}, nil
}

// moduleDistQuery scopes the distribution queries: the module's issues
// (Issue.issue_objects, through a live ModuleIssue) in the project.
type moduleDistQuery struct {
	slug      string
	projectID uuid.UUID
	moduleID  uuid.UUID
	tz        string // the active timezone, for TruncDate
}

// from is the FROM/WHERE the distributions share, parameters $1 module,
// $2 project, $3 slug.
func (q moduleDistQuery) from(joins string, liveLink bool) string {
	live := ""
	if liveLink {
		live = " AND mi.deleted_at IS NULL"
	}
	return ` FROM issues i JOIN module_issues mi ON mi.issue_id = i.id JOIN workspaces w ON w.id = i.workspace_id` + joins + `
		WHERE ` + issueObjects("i") + live + ` AND mi.module_id = $1 AND i.project_id = $2 AND w.slug = $3`
}

// moduleDistAggs are the distributions' total/completed/pending
// annotations: Count("id") filtered on archived_at and is_draft, or
// Sum(Cast("estimate_point__value")) whose total has no filter.
func moduleDistAggs(estimate bool) (aggs, joins string, names [3]string) {
	const completed = ` FILTER (WHERE i.archived_at IS NULL AND i.completed_at IS NOT NULL AND NOT i.is_draft)`
	const pending = ` FILTER (WHERE i.archived_at IS NULL AND i.completed_at IS NULL AND NOT i.is_draft)`
	if estimate {
		agg := "sum(ep.value::double precision)"
		return agg + ", " + agg + completed + ", " + agg + pending,
			` LEFT JOIN estimate_points ep ON ep.id = i.estimate_point_id`,
			[3]string{"total_estimates", "completed_estimates", "pending_estimates"}
	}
	agg := "count(i.id)"
	return agg + ` FILTER (WHERE i.archived_at IS NULL AND NOT i.is_draft), ` + agg + completed + ", " + agg + pending, "",
		[3]string{"total_issues", "completed_issues", "pending_issues"}
}

// moduleDistValues scans the three annotations: ints for counts, nullable
// floats for sums.
func moduleDistValues(estimate bool) ([3]any, func() [3]any) {
	if estimate {
		var f [3]*float64
		return [3]any{&f[0], &f[1], &f[2]}, func() [3]any { return [3]any{f[0], f[1], f[2]} }
	}
	var k [3]int
	return [3]any{&k[0], &k[1], &k[2]}, func() [3]any { return [3]any{k[0], k[1], k[2]} }
}

// moduleAssigneeDistribution is the assignee_distribution queryset: one
// row per assignee (through issue_assignees, deleted rows included) and
// one for unassigned issues, ordered by name.
func (a *API) moduleAssigneeDistribution(ctx context.Context, q moduleDistQuery, estimate bool) ([]map[string]any, error) {
	aggs, joins, names := moduleDistAggs(estimate)
	rows, err := a.db.Query(ctx, `SELECT u.first_name, u.last_name, ia.assignee_id,
			CASE WHEN u.avatar_asset_id IS NOT NULL THEN '/api/assets/v2/static/' || u.avatar_asset_id::varchar || '/'
				WHEN u.avatar_asset_id IS NULL THEN u.avatar ELSE NULL END,
			u.display_name, `+aggs+
		q.from(` LEFT JOIN issue_assignees ia ON ia.issue_id = i.id LEFT JOIN users u ON u.id = ia.assignee_id`+joins, true)+`
		GROUP BY 1, 2, 3, 5, 4 ORDER BY 1, 2`, q.moduleID, q.projectID, q.slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			first, last, avatar, display *string
			id                           *uuid.UUID
		)
		dst, vals := moduleDistValues(estimate)
		if err := rows.Scan(&first, &last, &id, &avatar, &display, dst[0], dst[1], dst[2]); err != nil {
			return nil, err
		}
		n := vals()
		out = append(out, map[string]any{"first_name": first, "last_name": last, "assignee_id": id,
			"avatar_url": avatar, "display_name": display, names[0]: n[0], names[1]: n[1], names[2]: n[2]})
	}
	return out, rows.Err()
}

// moduleLabelDistribution is the label_distribution queryset: one row per
// label (through issue_labels, deleted rows included) and one for issues
// without labels, ordered by name.
func (a *API) moduleLabelDistribution(ctx context.Context, q moduleDistQuery, estimate, liveLink bool) ([]map[string]any, error) {
	aggs, joins, names := moduleDistAggs(estimate)
	rows, err := a.db.Query(ctx, `SELECT l.name, l.color, il.label_id, `+aggs+
		q.from(` LEFT JOIN issue_labels il ON il.issue_id = i.id LEFT JOIN labels l ON l.id = il.label_id`+joins, liveLink)+`
		GROUP BY 1, 2, 3 ORDER BY 1`, q.moduleID, q.projectID, q.slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			name, color *string
			id          *uuid.UUID
		)
		dst, vals := moduleDistValues(estimate)
		if err := rows.Scan(&name, &color, &id, dst[0], dst[1], dst[2]); err != nil {
			return nil, err
		}
		n := vals()
		out = append(out, map[string]any{"label_name": name, "color": color, "label_id": id,
			names[0]: n[0], names[1]: n[1], names[2]: n[2]})
	}
	return out, rows.Err()
}

// moduleBurndown is utils.analytics_plot.burndown_plot for a module: for
// each day from start to target date, what is left once the issues (or
// their points) completed by that day are taken off; days after today
// (UTC) are null. Completion days are read in the active timezone
// (TruncDate).
func (a *API) moduleBurndown(ctx context.Context, q moduleDistQuery, m *moduleRow, points bool) (map[string]any, error) {
	type done struct {
		date  *time.Time
		value string // the estimate value, or the count
	}
	pyFloat := func(v string) (float64, error) {
		f, ok := drf.PyFloat(drf.JSONValue(jsonString(v)))
		if !ok {
			return 0, errViewCrash // float(value)
		}
		return f, nil
	}
	var (
		total     float64
		completed []done
		rows      pgx.Rows
		err       error
	)
	if points {
		rows, err = a.db.Query(ctx, `SELECT ep.value`+q.from(` JOIN estimate_points ep ON ep.id = i.estimate_point_id`, true)+`
			ORDER BY i.created_at DESC`, q.moduleID, q.projectID, q.slug)
		if err != nil {
			return nil, err
		}
		values, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		for _, v := range values {
			f, err := pyFloat(v)
			if err != nil {
				return nil, err
			}
			total += f
		}
		rows, err = a.db.Query(ctx, `SELECT (i.completed_at AT TIME ZONE $4)::date, ep.value`+
			q.from(` JOIN estimate_points ep ON ep.id = i.estimate_point_id`, true)+`
			ORDER BY 1`, q.moduleID, q.projectID, q.slug, q.tz)
	} else {
		total = float64(m.Issues[5])
		rows, err = a.db.Query(ctx, `SELECT (i.completed_at AT TIME ZONE $4)::date, count(i.id)::text`+q.from("", true)+`
			GROUP BY 1 ORDER BY 1`, q.moduleID, q.projectID, q.slug, q.tz)
	}
	if err != nil {
		return nil, err
	}
	completed, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (done, error) {
		var d done
		err := row.Scan(&d.date, &d.value)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	today := today()
	chart := map[string]any{}
	for d := m.StartDate.Time; !d.After(m.TargetDate.Time); d = d.AddDate(0, 0, 1) {
		var sum float64
		for _, item := range completed {
			if item.date != nil && !item.date.After(d) {
				f, err := pyFloat(item.value)
				if err != nil {
					return nil, err
				}
				sum += f
			}
		}
		key := d.Format(time.DateOnly)
		if key > today {
			chart[key] = nil
		} else {
			chart[key] = total - sum
		}
	}
	return chart, nil
}
