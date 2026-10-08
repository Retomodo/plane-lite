# Cycles (PORTING.md section 8)

## 1. Endpoints ported and goldens

| Endpoint | Golden |
|---|---|
| `GET`, `POST P/cycles/`; `GET`, `PATCH`, `DELETE P/cycles/<pk>/` | `cycle_crud` (147 steps) |
| `POST P/cycles/date-check/` | `cycle_crud` |
| `GET`, `PATCH P/cycles/<cycle_id>/user-properties/` | `cycle_crud` |
| `POST`, `DELETE P/cycles/<cycle_id>/archive/`; `GET P/archived-cycles/`, `P/archived-cycles/<pk>/` | `cycle_crud`, `cycle_analytics` |
| `GET W/cycles/` | `cycle_crud` |
| `GET`, `POST P/cycles/<cycle_id>/cycle-issues/`; `DELETE .../cycle-issues/<issue_id>/` | `cycle_issues` (129 steps) |
| `POST P/cycles/<cycle_id>/transfer-issues/` | `cycle_issues`, `cycle_analytics` |
| `GET P/issues/?cycle=` (already in issueQuery) | `cycle_issues` |
| `GET P/cycles/<cycle_id>/progress/`, `GET .../analytics/` | `cycle_analytics` (92 steps) |

Files: `internal/api/{cycle,cycle_issue,cycle_archive,cycle_analytics,issue_activity_cycle}.go`,
`routes_cycle.go`, `contract/cycle_test.go`.

The `cycle_crud` scenario runs its project on Asia/Kolkata so that the timezone conversions
(`convert_to_utc` on write, `user_timezone_converter` on read) show up. The goldens use dates
relative to today (`<today+N>` aliases), so they contain no literal dates.

## 2. Not ported

- UNUSED: `PUT P/cycles/<pk>/`, `PUT .../cycle-issues/`, and `GET`/`PATCH .../cycle-issues/<issue_id>/`.
- Unused variants: `GET P/cycles/<cycle_id>/archive/`, and `POST`/`DELETE` on `P/archived-cycles/`
  and `P/archived-cycles/<pk>/`.
- Methods that aren't registered answer 405 in Go.

## 3. Deviations

- `model_activity` (the webhook task) is not sent on create and update. This is skipped by the
  porting brief.
- The issue_activity task runs from the job queue, not Celery eager. The `cycle.activity.deleted`
  sent by cycle delete may run after the cycle is gone. It then uses the `cycle_name` the view sends,
  which is the same name, so the result is identical.
- Transfer's `cycle.activity.created` passes `created_cycle_issues` as a list. Django's task then
  crashes on `json.loads([])`, so no activity is written. Go aborts the same way.

## 4. Shared-file changes

- `internal/api/issue_activity.go`: added one `case` that dispatches `cycle.activity.created` and
  `cycle.activity.deleted` to `t.cycleActivity` (in `issue_activity_cycle.go`).
- No changes to `issue_list.go` or `issue_query.go`. The cycle issue list builds an `issueList`
  with `plainCounts` and `archived` set. `archived` reproduces the 500 that Django raises when
  grouping by a date or the creator, because `issue_group_values` gets no queryset there.

## 5. Test loosening and SQL in scenarios

- `Unordered` on `assignee_ids`, `label_ids` and `module_ids`. `array_agg(DISTINCT uuid)` orders by
  uuid value, which varies from run to run.
- `Mask` on `*.labels__id`, `*.assignees__id` and `*.issue_module__module_id` in grouped cycle-issue
  lists. Postgres picks which tied relation row of an issue shows first.
- `Mask("archived_at")` on the archive POST, which returns `str(timezone.now())`.
- `Mask("token")` on the `users/me` patches.
- SQL writes:
  - `INSERT INTO user_favorites ... RETURNING` (via `favoriteCycle`), because favorites are another
    area.
  - `UPDATE issues SET completed_at = now() - interval '2 days'`, to put completions on an earlier
    burndown day.
- SQL reads: the ids of a draft issue and an archived issue (Django's issue create returns 500 for
  them), and the estimate point ids.
- In the analytics scenario, alice and bob get first names, so that the archive detail's
  assignees (ordered by `first_name, last_name`) don't tie.

## 6. Uncertainties

- A non-numeric estimate value would make the burndown's `float()` raise, and the view would
  return 500. Go does the same through `drf.PyFloat`, but no scenario covers it.
- Removing a cycle issue that doesn't exist sends an activity for a missing issue. Its insert fails
  the foreign key, so neither side writes anything. This is logged in Go.
