package api

import "plane-lite/server/internal/jobs"

// ROADMAP.md "2. Scheduled jobs" / PORTING.md "Periodic": Celery beat's
// schedule (plane/celery.py), run by the River leader. Times are UTC. One
// porting agent owns this file and the scheduled*.go files.

func (a *API) registerScheduledJobs() {
	jobs.Register(a.jobs, a.runStackEmailNotification)
	jobs.Register(a.jobs, a.runSendEmailNotification)
	jobs.Register(a.jobs, a.runHardDelete)
	jobs.Register(a.jobs, a.runArchiveAndCloseOldIssues)
	jobs.Register(a.jobs, a.runDeleteEmailNotificationLogs)
	jobs.Register(a.jobs, a.runDeletePageVersions)
	jobs.Register(a.jobs, a.runDeleteIssueDescriptionVersions)

	jobs.RegisterPeriodic(a.jobs, "*/5 * * * *", stackEmailNotificationJob{})
	jobs.RegisterPeriodic(a.jobs, "0 0 * * *", hardDeleteJob{})
	jobs.RegisterPeriodic(a.jobs, "0 1 * * *", archiveAndCloseJob{})
	jobs.RegisterPeriodic(a.jobs, "0 2 * * *", unuploadedAssetsJob{}) // registered in routes_asset.go
	jobs.RegisterPeriodic(a.jobs, "45 2 * * *", deleteEmailNotificationLogsJob{})
	jobs.RegisterPeriodic(a.jobs, "0 3 * * *", deletePageVersionsJob{})
	jobs.RegisterPeriodic(a.jobs, "15 3 * * *", deleteIssueDescriptionVersionsJob{})
}
