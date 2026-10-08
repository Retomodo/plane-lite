package api

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"plane-lite/server/internal/harddelete"
)

// hardDeleteJob is bgtasks.deletion_task.hard_delete.
type hardDeleteJob struct{}

func (hardDeleteJob) Kind() string { return "hard_delete" }

// hardDeleteFirst are the models hard_delete names before its sweep over
// every model with deleted_at, in its order.
var hardDeleteFirst = []string{
	"workspaces", "projects", "cycles", "modules", "issues", "pages", "issue_views", "labels", "states",
	"issue_activities", "issue_comments", "issue_links", "issue_reactions", "user_favorites", "module_issues",
	"cycle_issues", "estimates", "estimate_points",
}

// runHardDelete ports hard_delete: for each model in turn,
// Model.all_objects.filter(deleted_at__lt=now - HARD_DELETE_AFTER_DAYS).delete(),
// each its own transaction with Django's cascade. Nothing catches an
// error, so a failing model ends the run; the ones before it stay deleted.
// Unlike Django, the issue_activities rows of a due issue or comment are
// deleted with it (see package harddelete), so that failure, which in
// Django recurs every night once such a row exists, doesn't happen.
func (a *API) runHardDelete(ctx context.Context, _ hardDeleteJob) error {
	for _, table := range append(append([]string{}, hardDeleteFirst...), harddelete.SoftDeletable...) {
		cutoff := time.Now().Add(-time.Duration(a.cfg.HardDeleteAfterDays) * 24 * time.Hour)
		if _, err := harddelete.Where(ctx, a.db, table, "deleted_at < $1", cutoff); err != nil {
			return fmt.Errorf("hard_delete %s: %w", table, err)
		}
	}
	return nil
}

// scheduledCleanupBatch is cleanup_task.BATCH_SIZE.
const scheduledCleanupBatch = 500

// scheduledCleanup ports cleanup_task.process_cleanup_task: it lists the ids to
// remove, then hard-deletes them (all_objects.filter(id__in=batch).delete())
// 500 at a time. A failed batch is logged and skipped.
func (a *API) scheduledCleanup(ctx context.Context, task, table, idsSQL string, args ...any) error {
	rows, err := a.db.Query(ctx, idsSQL, args...)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	deleted, batches := 0, 0
	for start := 0; start < len(ids); start += scheduledCleanupBatch {
		batch := ids[start:min(start+scheduledCleanupBatch, len(ids))]
		batches++
		n, err := harddelete.Where(ctx, a.db, table, "id = ANY($1::text[]::uuid[])", batch)
		if err != nil {
			a.log.Error("cleanup batch failed", "task", task, "err", err)
			continue
		}
		deleted += n
	}
	a.log.Info(task+" cleanup task completed", "total_records_deleted", deleted, "total_batches", batches)
	return nil
}

// deleteEmailNotificationLogsJob is cleanup_task.delete_email_notification_logs:
// logs sent more than EMAIL_LOG_RETENTION_DAYS ago.
type deleteEmailNotificationLogsJob struct{}

func (deleteEmailNotificationLogsJob) Kind() string { return "delete_email_notification_logs" }

func (a *API) runDeleteEmailNotificationLogs(ctx context.Context, _ deleteEmailNotificationLogsJob) error {
	cutoff := time.Now().Add(-time.Duration(a.cfg.EmailLogRetentionDays) * 24 * time.Hour)
	return a.scheduledCleanup(ctx, "Email Notification Log", "email_notification_logs",
		`SELECT id::text FROM email_notification_logs WHERE sent_at <= $1`, cutoff)
}

// deletePageVersionsJob is cleanup_task.delete_page_versions: all but each
// page's 20 newest versions (soft-deleted ones included).
type deletePageVersionsJob struct{}

func (deletePageVersionsJob) Kind() string { return "delete_page_versions" }

func (a *API) runDeletePageVersions(ctx context.Context, _ deletePageVersionsJob) error {
	return a.scheduledCleanup(ctx, "Page Version", "page_versions", `SELECT id::text FROM page_versions WHERE id IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY page_id ORDER BY created_at DESC) AS row_num
			FROM page_versions) v WHERE row_num > 20)`)
}

// deleteIssueDescriptionVersionsJob is
// cleanup_task.delete_issue_description_versions: all but each issue's 20
// newest description versions (soft-deleted ones included).
type deleteIssueDescriptionVersionsJob struct{}

func (deleteIssueDescriptionVersionsJob) Kind() string { return "delete_issue_description_versions" }

func (a *API) runDeleteIssueDescriptionVersions(ctx context.Context, _ deleteIssueDescriptionVersionsJob) error {
	return a.scheduledCleanup(ctx, "Issue Description Version", "issue_description_versions",
		`SELECT id::text FROM issue_description_versions WHERE id IN (
		SELECT id FROM (SELECT id, ROW_NUMBER() OVER (PARTITION BY issue_id ORDER BY created_at DESC) AS row_num
			FROM issue_description_versions) v WHERE row_num > 20)`)
}
