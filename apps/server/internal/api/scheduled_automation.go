package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// archiveAndCloseJob is bgtasks.issue_automation_task.archive_and_close_old_issues.
type archiveAndCloseJob struct{}

func (archiveAndCloseJob) Kind() string { return "archive_and_close_old_issues" }

func (a *API) runArchiveAndCloseOldIssues(ctx context.Context, _ archiveAndCloseJob) error {
	// Each half logs and swallows its own exception.
	if err := a.archiveOldIssues(ctx); err != nil {
		a.log.Error("archive_old_issues", "err", err)
	}
	if err := a.closeOldIssues(ctx); err != nil {
		a.log.Error("close_old_issues", "err", err)
	}
	return nil
}

// automationIssuesSQL is the issue_objects queryset both halves filter, as
// Django compiles it: the cycle, module and intake joins are LEFT JOINs over
// every row (soft-deleted ones too), so an issue in two cycles or modules
// comes back once per matching row and gets one activity per row.
const automationIssuesSQL = `SELECT i.id FROM issues i
	JOIN states s ON i.state_id = s.id
	JOIN projects p ON i.project_id = p.id
	LEFT JOIN cycle_issues ci ON i.id = ci.issue_id
	LEFT JOIN cycles c ON ci.cycle_id = c.id
	LEFT JOIN module_issues mi ON i.id = mi.issue_id
	LEFT JOIN modules m ON mi.module_id = m.id
	LEFT JOIN intake_issues ii ON i.id = ii.issue_id
	WHERE i.deleted_at IS NULL AND NOT (s."group" = 'triage' AND s."group" IS NOT NULL)
		AND NOT (i.archived_at IS NOT NULL) AND NOT (p.archived_at IS NOT NULL) AND NOT i.is_draft
		AND i.archived_at IS NULL AND i.project_id = $1 AND s."group" = ANY($2) AND i.updated_at <= $3
		AND (ci.id IS NULL OR (c.end_date < $4 AND ci.id IS NOT NULL))
		AND (mi.id IS NULL OR (m.target_date < $5 AND mi.id IS NOT NULL))
		AND (ii.status = 1 OR ii.status = -1 OR ii.status = 2 OR ii.id IS NULL)
	ORDER BY i.created_at DESC`

// automationNotifies is false although the task passes notification=True:
// it also passes issue_id as a UUID (Celery's JSON round-trips UUIDs), and
// the notification task compares each activity's issue_detail.id string
// with it, which never matches, so it writes no notification or email log.
const automationNotifies = false

type automationProject struct {
	id           uuid.UUID
	createdBy    *uuid.UUID
	months       int
	defaultState *uuid.UUID
}

// automationIssues runs automationIssuesSQL for one project: issues in
// groups not updated for months*30 days.
func (a *API) automationIssues(ctx context.Context, p automationProject, groups []string) ([]uuid.UUID, error) {
	now := time.Now().UTC()
	rows, err := a.db.Query(ctx, automationIssuesSQL, p.id, groups, now.AddDate(0, 0, -p.months*30), now,
		scheduledDate(now))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
}

// scheduledDate is timezone.now().date().
func scheduledDate(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// actorStr is str(project.created_by_id).
func (p automationProject) actorStr() string {
	if p.createdBy == nil {
		return "None"
	}
	return p.createdBy.String()
}

func (a *API) automationProjects(ctx context.Context, column string) ([]automationProject, error) {
	rows, err := a.db.Query(ctx, `SELECT id, created_by_id, `+column+`, default_state_id FROM projects
		WHERE deleted_at IS NULL AND `+column+` > 0 ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (automationProject, error) {
		var p automationProject
		err := r.Scan(&p.id, &p.createdBy, &p.months, &p.defaultState)
		return p, err
	})
}

// archiveOldIssues ports archive_old_issues: completed and cancelled issues
// untouched for archive_in months are archived (bulk_update, so updated_at
// is left alone) and each gets an automation activity from the project's
// creator.
func (a *API) archiveOldIssues(ctx context.Context) error {
	projects, err := a.automationProjects(ctx, "archive_in")
	if err != nil {
		return err
	}
	for _, p := range projects {
		issues, err := a.automationIssues(ctx, p, []string{"completed", "cancelled"})
		if err != nil {
			return err
		}
		if len(issues) == 0 {
			continue
		}
		today := scheduledDate(time.Now())
		archiveAt := today.Format(time.DateOnly)
		if _, err := a.db.Exec(ctx, `UPDATE issues SET archived_at = $2 WHERE id = ANY($1) AND deleted_at IS NULL`,
			issues, today); err != nil {
			return err
		}
		requested := `{"archived_at": "` + archiveAt + `", "automation": true}`
		current := `{"archived_at": null}`
		for _, id := range issues {
			a.enqueueIssueActivity(ctx, issueActivityJob{
				Type: "issue.activity.updated", RequestedData: &requested, CurrentInstance: &current,
				IssueID: id.String(), ActorID: p.actorStr(), ProjectID: p.id.String(),
				Epoch: time.Now().Unix(), Subscriber: false, Notification: automationNotifies,
			})
		}
	}
	return nil
}

// closeOldIssues ports close_old_issues: backlog, unstarted and started
// issues untouched for close_in months move to the project's default_state
// (soft-deleted or not: select_related doesn't filter). Without one, Django
// takes the first cancelled state of any project, by sequence; the activity
// then fails to find that state in the issue's project and is not written,
// but the issue has moved.
func (a *API) closeOldIssues(ctx context.Context) error {
	projects, err := a.automationProjects(ctx, "close_in")
	if err != nil {
		return err
	}
	for _, p := range projects {
		issues, err := a.automationIssues(ctx, p, []string{"backlog", "unstarted", "started"})
		if err != nil {
			return err
		}
		if len(issues) == 0 {
			continue
		}
		closeState := p.defaultState
		if closeState == nil {
			var id uuid.UUID
			err := a.db.QueryRow(ctx, `SELECT id FROM states WHERE deleted_at IS NULL AND NOT ("group" = 'triage')
				AND "group" = 'cancelled' ORDER BY sequence ASC LIMIT 1`).Scan(&id)
			switch {
			case err == nil:
				closeState = &id
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
		}
		if _, err := a.db.Exec(ctx, `UPDATE issues SET state_id = $2 WHERE id = ANY($1) AND deleted_at IS NULL`,
			issues, closeState); err != nil {
			return err
		}
		closedTo := "None"
		if closeState != nil {
			closedTo = closeState.String()
		}
		raw, _ := json.Marshal(closedTo)
		requested := `{"closed_to": ` + string(raw) + `}`
		for _, id := range issues {
			a.enqueueIssueActivity(ctx, issueActivityJob{
				Type: "issue.activity.updated", RequestedData: &requested, CurrentInstance: nil,
				IssueID: id.String(), ActorID: p.actorStr(), ProjectID: p.id.String(),
				Epoch: time.Now().Unix(), Subscriber: false, Notification: automationNotifies,
			})
		}
	}
	return nil
}
