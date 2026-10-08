# Scheduled jobs (ROADMAP "2. Scheduled jobs", PORTING.md "Periodic")

## 1. Jobs ported

Celery beat's schedule (`plane/celery.py`) as River periodic jobs. Times are UTC, as in beat. Each job is named
after its Django task function, which is also its River kind and its `plane run-job` name.

| Schedule (UTC) | Job (`plane run-job <name>`) | Django task | Golden |
|---|---|---|---|
| `*/5 * * * *` | `stack_email_notification`, which queues `send_email_notification` | `email_notification_task.stack_email_notification` → `send_email_notification` | `scheduled_email_digest` (54 steps), `scheduled_cleanup` |
| `0 0 * * *` | `hard_delete` | `deletion_task.hard_delete` | `scheduled_hard_delete` (74); Go-only `TestScheduledHardDeleteDeviation` (see 3) |
| `0 1 * * *` | `archive_and_close_old_issues` | `issue_automation_task.archive_and_close_old_issues` | `scheduled_archive_close` (66) |
| `45 2 * * *` | `delete_email_notification_logs` | `cleanup_task.delete_email_notification_logs` | `scheduled_cleanup` (51) |
| `0 3 * * *` | `delete_page_versions` | `cleanup_task.delete_page_versions` | `scheduled_cleanup` |
| `15 3 * * *` | `delete_issue_description_versions` | `cleanup_task.delete_issue_description_versions` | `scheduled_cleanup` |

Code:
- `internal/api/routes_scheduled.go` registers the jobs and their schedules (`registerScheduledJobs`, wired in
  `api.go`).
- `scheduled_email.go` holds the digest, `scheduled_cleanup.go` the hard delete and the three cleanups, and
  `scheduled_automation.go` the archive/close automation.
- `internal/harddelete` ports Django's deletion Collector (see 4).
- `internal/mail/django.go` renders the digest template (see 4).
- Scenarios are in `contract/scheduled_test.go`.

Unit tests:
- `internal/jobs/periodic_test.go`: slot computation, registration, and a River test on its own database
  (`plane_jobs_test` on the slot's Postgres). It checks that a starting leader runs the missed slot once, and
  that a restart doesn't run it again.
- `internal/mail/django_test.go`: Go's render of the digest template must match Django's `render_to_string`
  byte for byte, over 7 contexts that cover every branch. The fixtures come from
  `contract/reference/gen_issue_updates_fixtures.py`.

### Scheduling, and what happens when a run is missed or doubled

- `jobs.RegisterPeriodic(r, "0 2 * * *", someJob{})` adds a schedule in one line. The job must already be
  registered with `jobs.Register`. This is the hook for the assets agent's
  `delete_unuploaded_file_asset` (02:00).
- Only the elected River leader enqueues, so a schedule runs on one replica only.
- Each enqueued run is `periodicArgs{Job, Slot}`. `Slot` is the scheduled instant the run stands for.
  River unique-by-args (states include completed, which River keeps for 24 h) drops a second insert of the same
  slot. The slot is computed 1 s ahead, because River fires up to 100 ms early.
- **Missed runs.** Cloudflare Containers may sleep, and restarts happen. A leader that starts up enqueues the
  most recent slot of every schedule (`RunOnStart`). If that slot already ran, it is a duplicate and is dropped.
  So a process that slept through 00:00 runs `hard_delete` once when it wakes, at whatever time that is. Older
  missed slots are skipped. That is safe because every job is a sweep ("older than N days", "not processed
  yet"), and the next run does what the missed one would have. Celery beat does the same: one catch-up run
  per missed entry.
  - The digest mails late, bundling everything queued in the meantime.
  - Auto-archive stamps the day it actually runs.
- **Sleeping containers.** A sleeping container is not woken by these schedules. If the app gets no traffic,
  the nightly jobs run at the next wake-up.
- **Doubled runs** are harmless:
  - The sweeps are idempotent.
  - Archive/close finds nothing the second time (archived issues are excluded, and closing bumps
    `updated_at`).
  - The digest keeps Django's guards. `processed_at` keeps a log from being stacked twice. The 5-minute
    Redis lock `send_email_notif_<issue>_<receiver>_<sorted ids>` (with `REDIS_KEY_PREFIX`) stops two sends of
    the same logs at once. A failed send releases it. On top of these, the slot dedupe stops two stack runs for
    the same 5 minutes.
- Periodic runs have `MaxAttempts: 1`, like Celery, which doesn't retry: the next slot is the retry.
  `send_email_notification` keeps the email jobs' 3 attempts (see 3).
- Tests: `jobs.New(…, inline=true)` doesn't start River, and `Runner.RunPeriodic(ctx, name)` runs a job
  synchronously.
- `plane run-job <name>` builds the server with inline jobs, so whatever the job enqueues (issue activity,
  digest mails) also runs before it exits, as with Celery eager. It is hidden: the unknown-command message
  doesn't mention it. It is listed in the README layout table.

### Settings (env, Django's names and defaults; `internal/config`, README)

- `HARD_DELETE_AFTER_DAYS` (60).
- `EMAIL_LOG_RETENTION_DAYS` (7). A negative value falls back to the default, as `_retention_days` does.
- The schedules themselves are hard-coded, as in `celery.py`.
- The page and description version cap (20) is hard-coded in Django too.

## 2. Not ported

- `delete_unuploaded_file_asset` (02:00) belongs to the assets agent.
- Telemetry, exporter link expiry, API log and webhook log cleanup are cut.

## 3. Deviations

| Where | Django | Go | Why |
|---|---|---|---|
| Digest links and the origin key | `send_email_notification` reads the request origin that `issue_activity` caches in Redis for **10 minutes** (`ri.set(issue_id, origin, ex=600)`). Without it, the mail is silently skipped: the logs stay processed, are never sent, and the lock is not released. So a digest that runs more than 10 minutes after the issue's last request-driven activity is dropped (beat running late, or a woken container). | Always mails, with `APP_BASE_URL` (`baseHost(true)`, which is what Django caches anyway) | Follows the existing "Issue origin cache" deviation (Go never writes the key). The goldens agree because the scenarios run the digest within seconds. |
| Digest send failure | SMTP error: logged, lock released, the logs stay unsent forever | The job fails and retries (3 attempts, `emailInsertOpts`); the lock is released first. A failure *after* the mail went out (marking `sent_at`) is only logged, so the mail is never sent twice. | Same as the other email jobs (DEVIATIONS "Background jobs") |
| Mentions in the digest's comment HTML | BeautifulSoup replaces `<mention-component>` with `@display_name` and re-serializes the whole fragment | The mention tags are replaced in place; the rest of the markup is kept as written | Same text part, which is what the golden compares. The HTML part may differ in serialization details (attribute quoting, `<br/>`, entity forms). |
| Receiver order in the digest | Python set order (random per process) | First appearance in the log query | Each receiver's mail is independent |
| hard_delete and rows that point at a due issue or comment | `issue_activities.issue_id` and `.issue_comment_id` are `on_delete=DO_NOTHING` with real deferred foreign keys. Hard-deleting an issue or comment that still has activities fails at commit (IntegrityError). Nothing catches it, so the run stops at that model (`issues`, or `issue_comments`). It fails again every night, so once any individually deleted issue or comment is past the cutoff, nothing after it in the order is ever purged again: pages, views, labels, states, links, reactions, favorites, cycle and module issues, estimates, and the whole final sweep. | The DO_NOTHING referrers are deleted along with the row, like CASCADE children (`internal/harddelete`). These two are the only DO_NOTHING relations in the model registry, and no SET_NULL column is NOT NULL. Every run completes end to end. Everything else stays Django's: the order, one transaction per model, CASCADE, SET_NULL, and the cutoff. | User decision: Django's job never completes once such a row exists. Django's failure was confirmed when recording (IntegrityError on `issue_activities_issue_id_…_fk_issues_id` and `issue_activity_issue_comment_id_…_fk_issue_comment_id`). Since no golden can show the fix, the Django-recorded failure scenarios were dropped. The Go-only `TestScheduledHardDeleteDeviation` (`contract.RunGoOnly`, skipped when recording) checks the fixed run: a due deleted issue (with a sub-issue and a comment) and a due deleted comment on a live issue are purged with their activities and the comment's reaction, the live issue keeps its other activities, a due module (before `issues`), a due label (after `issue_comments`) and an unassigned `issue_assignees` row (final sweep) are purged too, no soft-deleted row is left, and a second run is ok. Without the fix the test fails at `issues`. `scheduled_hard_delete` (no due issue or comment) is still recorded from Django and still matches. |
| Scheduler | Celery beat process with `django_celery_beat.DatabaseScheduler` | River periodic jobs on the leader, in the server process | No beat or worker processes; see 1 for catch-up |

## 4. Quirks ported on purpose

### hard_delete (`scheduled_cleanup.go`, `internal/harddelete`)

- **How it deletes.** Each model is `Model.all_objects.filter(deleted_at__lt=cutoff).delete()`, in one
  transaction per model, through Django's Collector:
  - every CASCADE child is deleted, live or soft-deleted (`_base_manager`), recursively;
  - SET_NULL children are nulled, without touching `updated_at`;
  - DO_NOTHING children (only `issue_activities`) are **deleted** too. This is a deviation; see 3.
- **The relation table.** `internal/harddelete/relations_gen.go` is generated from Django's model registry by
  `contract/reference/gen_hard_delete.py`. It includes hidden relations (auto many-to-many tables) and lists
  the models of the final sweep in registry order. The generator refuses on_delete kinds other than these three,
  foreign keys to non-pk columns, and custom base managers.
- **Order.** The order is the task's: the 18 named models, then every model with `deleted_at`.
- **Nothing catches an error, so the first failing model ends the run.** The models before it stay deleted.
  In Django, the DO_NOTHING `issue_activities` foreign keys make this happen every night once a deleted issue
  or comment is due. Go purges those activities instead (3), so this case no longer arises.
- **Cascades into issues.** Hard-deleting a state cascades to the issues in it (`states → issues` is CASCADE).
  In Django those issues' activities then fail the same way; in Go they are purged. API-deleted states can't
  hold issues, so this doesn't happen in practice.
- **Deleted workspaces** have their slug renamed `<slug>__<epoch>` by the soft delete. The scenario compares the
  part before `__`.

### archive_and_close_old_issues (`scheduled_automation.go`)

- **Projects.** Projects come from `Project.objects` (not deleted), ordered by `-created_at`.
- **Which issues.** Issues come from `issue_objects` with Django's exact joins. Issues are "untouched" for
  `archive_in`/`close_in` × 30 days by `updated_at`. Archive takes completed and cancelled issues; close takes
  backlog, unstarted and started ones.
- **Cycle and module joins.** The cycle and module conditions are LEFT JOINs over every `cycle_issues` and
  `module_issues` row, soft-deleted ones included:
  - An issue removed from a module (or cycle) that hasn't ended is never archived or closed.
  - An issue in two ended modules comes back twice and gets two activities.

  Both are in the golden.
- **Archive.** Archiving is a `bulk_update` of `archived_at` = today (UTC), which leaves `updated_at` alone.
  Each issue gets `issue_activity` (`{"archived_at": "<date>", "automation": true}`) from the project's
  creator. That writes "Plane has archived the issue" and bumps `updated_at`.
- **Close.** Closing moves the issue to `project.default_state`. The state is loaded through `select_related`,
  so a soft-deleted state is still used.
  - Without a default_state, the close state is the first cancelled state **of any project**, by `sequence`.
  - If that state belongs to another project, the activity's `State.objects.get(pk, project_id)` fails. No
    activity is written, but the issue has moved into the other project's state. This is in the golden: the
    QT issue ends in `PL/Stale`.
- **No notifications.** The automation passes `notification=True`, but its `issue_id` is a UUID object, which
  Celery's JSON round-trips as a UUID. The notification task compares each activity's `issue_detail.id`
  (a string) with it, never matches, and writes no notification or email log. Go passes `Notification: false`,
  which has the same effect (`automationNotifies`).

### Digest (`scheduled_email.go`)

- **Grouping.** `stack_email_notification` reads unprocessed logs ordered by the receiver's `created_at DESC`
  (User's ordering) and groups them by receiver, then issue, then actor. It queues one send per (receiver,
  issue), then sets `processed_at` on all of them.
- **One mail marks every log sent.** Every send carries **all** of the receiver's log ids, both in the lock
  key and in the `sent_at` update. So the first mail that goes out marks all of that receiver's logs sent,
  including those of an issue whose own mail fails (for example, a deleted issue). This showed up while
  writing `scheduled_cleanup`, which therefore gives its unsent log to another receiver.
- **`create_payload`:**
  - It dedupes each field's old and new values.
  - `str()` turns None into `"None"`, and that string counts as a value.
  - Its activity-time guard reads the literal key `"actor_id"`, so the time shown is the last change's.
  - The time is printed `"%H:%M %p"`: a 24-hour clock with AM/PM ("21:30 PM").
  - A change with neither value, for an actor who has none yet, is a KeyError, so the mail is skipped.
- **Template.** It is rendered from Django's own template (copied verbatim to
  `internal/mail/templates/notifications/issue-updates.html`) by `mail.RenderDjango`. That is a small port of
  Django's template language: variables with dict and index lookups, the `length`, `add`, `last`, `slice` and
  `safe` filters, if/elif/else with smartif operators, `for`, and autoescape. The text part is `PlainText` of
  it, as before.
- **Skipped mails.** An issue deleted before the run, an unknown receiver, actor or mentioned user, or a
  missing `entity_identifier` skips the mail. The logs stay processed and unsent.

### Cleanups (`scheduled_cleanup.go`)

- **Email logs.** Only logs with `sent_at` older than the window are removed. Logs that were never sent stay
  forever.
- **Versions.** Page and description versions are ranked with `ROW_NUMBER() OVER (PARTITION BY … ORDER BY
  created_at DESC)` over all rows, soft-deleted ones included. Everything past 20 is removed.
- **Batches.** Rows are removed 500 at a time through the Collector. A failing batch is logged and skipped.

## 5. Changes to shared files

**Infrastructure:**
- `internal/jobs/jobs.go`:
  - adds a `periodic` map to `Runner`;
  - `New` registers the `periodic` dispatcher worker;
  - `Start` passes `PeriodicJobs`.
- New files: `internal/jobs/periodic.go` and `periodic_test.go`.
- `go.mod`: adds `github.com/robfig/cron/v3` (crontab parsing, the cron package River's docs recommend; it was
  already in go.sum).
- `internal/config/config.go`: adds `HardDeleteAfterDays` and `EmailLogRetentionDays`, plus the
  `retentionDays` helper.
- `internal/api/api.go`: one line, `a.registerScheduledJobs()`.
- `cmd/plane/main.go`: adds `run-job` (`run` now takes the remaining args).
- `README.md`: the settings, `run-job`, and the `internal/harddelete` row. The task asked for this, although
  the playbook leaves README to the lead.

**Mail:**
- New file: `internal/mail/django.go`.
- New files: `internal/mail/templates/notifications/issue-updates.html` (verbatim) and
  `internal/mail/testdata/issue_updates.json`.

**New package:** `internal/harddelete`.

**Harness (small, additive):**
- `contract/jobs.go` (new) adds `s.RunJob("<dotted Django task path>")`.
  - When recording, it runs the task in a Django shell on the reference (`scripts/devstack.sh <slot> compose
    exec -T reference python manage.py shell -c …`, Celery eager).
  - When verifying, it runs the Go job named after the path's last part (`Runner.RunPeriodic`, inline jobs).
  - It records a step `{actor: "job", method: "RUN", path: <task>, body: "ok" | "error"}`, and returns the
    result. The body is "error" when the task raised.
- `contract/jobs.go` also adds `RunGoOnly(t, fn)`: a scenario against the Go server with no golden, for a
  deliberate deviation. It is skipped when recording, and `fn` asserts the outcome itself.
- `contract/env.go`: `env.slot`.
- `contract/target.go`:
  - `target.jobs` (the Go server's runner);
  - the test config sets `HardDeleteAfterDays: 60` and `EmailLogRetentionDays: 7` (the reference's
    defaults).

**Generators:** `contract/reference/gen_hard_delete.py` and `gen_issue_updates_fixtures.py`.

## 6. Unsure / for the lead

- **Waking the container.** Cloudflare Containers don't wake for these schedules. If nightly timing matters
  more than "the next time someone uses the app", the Worker in front could ping the container from a Cron
  Trigger. Alternatively, ops can run `plane run-job <name>`, but a second process isn't a River leader.
- **Origin TTL.** The digest's origin-TTL difference (3) means Go sends some mails Django would silently drop:
  any digest that runs more than 10 minutes after the last request-driven activity. I think that is the right
  side to err on, but it is a visible behaviour change.
- **Unique-slot dedupe** relies on River keeping completed jobs for 24 h (the default
  `CompletedJobRetentionPeriod`). If someone lowers it below a day, a restart could repeat that day's daily
  jobs. That is harmless (they're sweeps), but worth knowing.
- **Ordering in the digest scenario.** Several values of one field from one actor (two priority changes, two
  assignees added at once) come out of Django in an order that varies from run to run. The reasons are the
  `ORDER BY users.created_at` ties and Python set order in `track_assignees`, and the mail shows the first or
  last value. The digest scenario therefore makes one change per field and actor. The template's multi-value
  branches are covered by the byte-exact template fixtures instead.
