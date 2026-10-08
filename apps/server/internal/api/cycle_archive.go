package api

import (
	"net/http"
	"time"

	"github.com/go-json-experiment/json/jsontext"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/httpx"
)

// CycleArchiveUnarchiveEndpoint (cycle/archive.py). Its get_queryset counts
// issues without looking at the cycle_issues rows' deleted_at, and its
// assignee_ids keep removed assignees.

// cycleArchived is one dict of the archive views' .values(): the list's
// fields, plus the detail's.
type cycleArchived struct {
	ID               uuid.UUID      `json:"id"`
	WorkspaceID      uuid.UUID      `json:"workspace_id"`
	ProjectID        uuid.UUID      `json:"project_id"`
	Name             string         `json:"name"`
	Description      string         `json:"description"`
	StartDate        *utcTime       `json:"start_date"`
	EndDate          *utcTime       `json:"end_date"`
	OwnedByID        uuid.UUID      `json:"owned_by_id"`
	ViewProps        jsontext.Value `json:"view_props"`
	SortOrder        float64        `json:"sort_order"`
	ExternalSource   *string        `json:"external_source"`
	ExternalID       *string        `json:"external_id"`
	ProgressSnapshot jsontext.Value `json:"progress_snapshot"`
	TotalIssues      int            `json:"total_issues"`
	IsFavorite       bool           `json:"is_favorite"`
	CancelledIssues  int            `json:"cancelled_issues"`
	CompletedIssues  int            `json:"completed_issues"`
	StartedIssues    int            `json:"started_issues"`
	UnstartedIssues  int            `json:"unstarted_issues"`
	BacklogIssues    int            `json:"backlog_issues"`
	AssigneeIDs      []uuid.UUID    `json:"assignee_ids"`
	Status           string         `json:"status"`
	ArchivedAt       *utcTime       `json:"archived_at"`
}

// cycleArchivedDetail is the detail's dict.
type cycleArchivedDetail struct {
	cycleArchived
	SubIssues               int            `json:"sub_issues"`
	LogoProps               jsontext.Value `json:"logo_props"`
	CompletedEstimatePoints float64        `json:"completed_estimate_points"`
	TotalEstimatePoints     float64        `json:"total_estimate_points"`
	CreatedBy               *uuid.UUID     `json:"created_by"`
	EstimateDistribution    map[string]any `json:"estimate_distribution"`
	Distribution            map[string]any `json:"distribution"`
}

// cycleArchivedSQL is the archive get_queryset's .values() for user $1 in
// project $3 of workspace $2, statuses at $4; detail adds the detail's
// fields for cycle $5.
func cycleArchivedSQL(detail bool) string {
	const live = `i.archived_at IS NULL AND i.deleted_at IS NULL AND NOT i.is_draft`
	group := func(g string) string {
		return `COUNT(DISTINCT ci.issue_id) FILTER (WHERE ` + live + ` AND st."group" = '` + g + `')`
	}
	estimate := func(group string) string {
		cond, join := "", "LEFT JOIN"
		if group != "" {
			cond, join = ` AND es."group" = '`+group+`'`, "JOIN"
		}
		return `COALESCE((SELECT SUM(eep.value::double precision) FROM issues ei ` + join + ` states es ON es.id = ei.state_id
			JOIN projects ep ON ep.id = ei.project_id JOIN estimate_points eep ON eep.id = ei.estimate_point_id
			JOIN estimates ee ON ee.id = eep.estimate_id JOIN cycle_issues eci ON eci.issue_id = ei.id
			WHERE ei.deleted_at IS NULL AND es."group" IS DISTINCT FROM 'triage' AND ei.archived_at IS NULL
				AND ep.archived_at IS NULL AND NOT ei.is_draft AND ee.type = 'points' AND eci.cycle_id = c.id
				AND eci.deleted_at IS NULL` + cond + `
			GROUP BY eci.cycle_id LIMIT 1), 0.0)`
	}
	sql := `SELECT c.id, c.workspace_id, c.project_id, c.name, c.description, c.start_date, c.end_date, c.owned_by_id,
			c.view_props, c.sort_order, c.external_source, c.external_id, c.progress_snapshot,
			COUNT(DISTINCT ci.issue_id) FILTER (WHERE ` + live + `), ` + cycleFavoriteSQL + `,
			` + group("cancelled") + `, ` + group("completed") + `, ` + group("started") + `, ` + group("unstarted") + `,
			` + group("backlog") + `,
			COALESCE(array_agg(DISTINCT ia.assignee_id) FILTER (WHERE ia.assignee_id IS NOT NULL), '{}'),
			` + cycleStatusSQL + `, c.archived_at`
	extra := ""
	if detail {
		sql += `, (SELECT count(*) FROM issues si LEFT JOIN states sst ON sst.id = si.state_id
				JOIN projects sp ON sp.id = si.project_id JOIN cycle_issues sci ON sci.issue_id = si.id
				WHERE si.deleted_at IS NULL AND sst."group" IS DISTINCT FROM 'triage' AND si.archived_at IS NULL
					AND sp.archived_at IS NULL AND NOT si.is_draft AND sci.cycle_id = $5 AND sci.deleted_at IS NULL
					AND si.parent_id IS NOT NULL AND si.project_id = $3),
			c.logo_props, ` + estimate("completed") + `, ` + estimate("") + `, c.created_by_id`
		extra = ` AND c.id = $5`
	}
	return sql + `
		FROM cycles c JOIN workspaces w ON w.id = c.workspace_id JOIN projects p ON p.id = c.project_id
			JOIN project_members pm ON pm.project_id = p.id
			LEFT JOIN cycle_issues ci ON ci.cycle_id = c.id LEFT JOIN issues i ON i.id = ci.issue_id
			LEFT JOIN states st ON st.id = i.state_id LEFT JOIN issue_assignees ia ON ia.issue_id = i.id
		WHERE c.deleted_at IS NULL AND w.slug = $2 AND c.project_id = $3 AND c.archived_at IS NOT NULL
			AND pm.is_active AND pm.member_id = $1 AND p.archived_at IS NULL` + extra + `
		GROUP BY c.id
		ORDER BY 15 DESC, c.created_at DESC`
}

func scanCycleArchived(row pgx.CollectableRow, detail bool) (*cycleArchivedDetail, error) {
	var (
		d          cycleArchivedDetail
		start, end *time.Time
		archivedAt *time.Time
		n          [6]int64
		sub        int64
	)
	v := &d.cycleArchived
	dst := []any{&v.ID, &v.WorkspaceID, &v.ProjectID, &v.Name, &v.Description, &start, &end, &v.OwnedByID,
		&v.ViewProps, &v.SortOrder, &v.ExternalSource, &v.ExternalID, &v.ProgressSnapshot, &n[0], &v.IsFavorite,
		&n[1], &n[2], &n[3], &n[4], &n[5], &v.AssigneeIDs, &v.Status, &archivedAt}
	if detail {
		dst = append(dst, &sub, &d.LogoProps, &d.CompletedEstimatePoints, &d.TotalEstimatePoints, &d.CreatedBy)
	}
	err := row.Scan(dst...)
	v.StartDate, v.EndDate, v.ArchivedAt = (*utcTime)(start), (*utcTime)(end), (*utcTime)(archivedAt)
	v.TotalIssues, v.CancelledIssues, v.CompletedIssues = int(n[0]), int(n[1]), int(n[2])
	v.StartedIssues, v.UnstartedIssues, v.BacklogIssues = int(n[3]), int(n[4]), int(n[5])
	d.SubIssues = int(sub)
	return &d, err
}

// archivedCycles ports CycleArchiveUnarchiveEndpoint.get: the archived
// cycles, or one with its distributions (pk not archived: a 500).
func (a *API) archivedCycles(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	slug := c.Param("slug")
	args := []any{c.User.ID, slug, projectID, time.Now()}
	if c.Param("pk") == "" {
		rows, err := a.db.Query(ctx, cycleArchivedSQL(false), args...)
		if err != nil {
			return err
		}
		out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*cycleArchived, error) {
			d, err := scanCycleArchived(row, false)
			return &d.cycleArchived, err
		})
		if err != nil {
			return err
		}
		if out == nil {
			out = []*cycleArchived{}
		}
		return c.JSON(http.StatusOK, out)
	}
	pk, err := c.UUIDParam("pk")
	if err != nil {
		return err
	}
	rows, err := a.db.Query(ctx, cycleArchivedSQL(true), append(args, pk)...)
	if err != nil {
		return err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (*cycleArchivedDetail, error) {
		return scanCycleArchived(row, true)
	})
	if err != nil {
		return err
	}
	if len(found) == 0 {
		return errViewCrash // data["estimate_distribution"] on None
	}
	v := found[0]
	pointsProject, err := a.cyclePointsProject(ctx, slug, projectID)
	if err != nil {
		return err
	}
	v.EstimateDistribution = map[string]any{}
	if pointsProject {
		as, err := a.cycleDistribution(ctx, slug, projectID, pk, cycleDist{points: true})
		if err != nil {
			return err
		}
		ls, err := a.cycleDistribution(ctx, slug, projectID, pk, cycleDist{labels: true, points: true})
		if err != nil {
			return err
		}
		var chart any = map[string]any{}
		if v.StartDate != nil && v.EndDate != nil {
			if chart, err = a.cycleBurndown(ctx, c, projectID, pk, (*time.Time)(v.StartDate), (*time.Time)(v.EndDate),
				v.TotalIssues, true); err != nil {
				return err
			}
		}
		v.EstimateDistribution = map[string]any{"assignees": as, "labels": ls, "completion_chart": chart}
	}
	as, err := a.cycleDistribution(ctx, slug, projectID, pk, cycleDist{count: "i.id", names: true})
	if err != nil {
		return err
	}
	ls, err := a.cycleDistribution(ctx, slug, projectID, pk, cycleDist{labels: true, count: "i.id"})
	if err != nil {
		return err
	}
	var chart any = map[string]any{}
	if v.StartDate != nil && v.EndDate != nil {
		if chart, err = a.cycleBurndown(ctx, c, projectID, pk, (*time.Time)(v.StartDate), (*time.Time)(v.EndDate),
			v.TotalIssues, false); err != nil {
			return err
		}
	}
	v.Distribution = map[string]any{"assignees": as, "labels": ls, "completion_chart": chart}
	return c.JSON(http.StatusOK, v)
}

// archiveCycle ports CycleArchiveUnarchiveEndpoint.post: completed cycles
// only; everyone's favorites of the cycle go.
func (a *API) archiveCycle(c *httpx.Ctx) error {
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
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if cy.End == nil || !cy.End.Before(now) {
		return httpx.Err(http.StatusBadRequest, "Only completed cycles can be archived")
	}
	if _, err := a.db.Exec(ctx, `UPDATE cycles SET archived_at = $2, updated_at = now(), updated_by_id = $3 WHERE id = $1`,
		cy.ID, now, c.User.ID); err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE user_favorites f SET deleted_at = now() FROM workspaces w
		WHERE w.id = f.workspace_id AND f.entity_type = 'cycle' AND f.entity_identifier = $1 AND f.project_id = $2
			AND w.slug = $3 AND f.deleted_at IS NULL`, cy.ID, projectID, slug); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"archived_at": pyDateTimeStr(now)})
}

// unarchiveCycle ports CycleArchiveUnarchiveEndpoint.delete.
func (a *API) unarchiveCycle(c *httpx.Ctx) error {
	projectID, err := c.UUIDParam("project_id")
	if err != nil {
		return err
	}
	cycleID, err := c.UUIDParam("cycle_id")
	if err != nil {
		return err
	}
	ctx := c.Context()
	cy, err := a.loadCycle(ctx, c.Param("slug"), projectID, cycleID)
	if err != nil {
		return err
	}
	if _, err := a.db.Exec(ctx, `UPDATE cycles SET archived_at = NULL, updated_at = now(), updated_by_id = $2 WHERE id = $1`,
		cy.ID, c.User.ID); err != nil {
		return err
	}
	return c.NoContent()
}
