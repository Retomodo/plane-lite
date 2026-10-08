# Misc (PORTING.md section 18): timezones and the profile pages

## 1. Endpoints ported

| Method | URL | View | Golden |
|---|---|---|---|
| GET | `/api/timezones/` | `TimezoneEndpoint.get` | `misc_timezones` (46 steps) + unit test `api.TestTimezoneList` |
| GET | `W/user-stats/<user_id>/` | `WorkspaceUserProfileStatsEndpoint.get` | `misc_profile` (150 steps) |
| GET | `W/user-activity/<user_id>/` | `WorkspaceUserActivityEndpoint.get` | `misc_profile` |
| POST | `W/user-activity/<user_id>/export/` | `ExportWorkspaceUserActivityEndpoint.post` | `misc_profile` |
| GET | `W/user-profile/<user_id>/` | `WorkspaceUserProfileEndpoint.get` | `misc_profile` |
| GET | `W/user-issues/<user_id>/` | `WorkspaceUserProfileIssuesEndpoint.get` | `misc_user_issues` (138 steps) |

Code: `internal/api/routes_misc.go`, `misc_timezone.go` (+ `misc_timezone_test.go`,
`testdata/timezones.json`), `misc_profile.go`. Scenarios: `contract/misc_test.go`.

Behaviour ported as found, including these quirks:

- **Timezones.** The page cache is keyed by the absolute URL only, so anonymous and signed-in users share it.
  It is stored in Redis for 2 h. A fresh page sends `Expires` and `Cache-Control: max-age=7200`; a cached one
  also sends `Age`. `AuthenticationThrottle` (anonymous only) runs before the cache and before the method check,
  so an anonymous POST/PUT/PATCH/DELETE counts against the limit and gets a 429 once over it. The offset text
  floors the hours, so UTC-09:30 reads `UTC-10:30`.
- **user-stats.** There is no permission class: outsiders and unknown slugs get zero counts. The cycle lists
  ignore the viewer's memberships and the filters, and repeat a cycle once per assignee row of the issue,
  removed rows included. The subscription count ignores issue state, deletion and drafts. The legacy filters also
  apply to `IssueSubscriber`, so any filter other than project, created_by or created_at/updated_at gives a 500
  (FieldError).
- **user-profile.** The per-project counts join every issue, deleted ones included, and every assignee row,
  which multiplies `created_issues`. Guests get `project_data: []`. `date_joined` is rendered in UTC.
- **user-activity and export.** Both join `project_members` without DISTINCT. Export includes archived projects,
  takes the date in the viewer's time zone and quotes formula-leading strings (`'@bob`). Bad dates give 400; a
  non-string date or a list body gives 500.
- **user-issues.** Reuses `issueList`/`issueQuery`. It has no DISTINCT, its counts are 0 rather than NULL, it
  skips the `updated_at__gt` extra filter and its group values are workspace-wide.

## 2. Not ported

UNUSED (no web/live caller), left unported: `GET /api/users/me/activities/`,
`GET /api/users/me/workspaces/<slug>/activity-graph/`, `GET …/issues-completed-graph/`, `GET …/dashboard/`.

## 3. Deviations

- **Timezone golden is masked.** It masks `utc_offset`/`gmt_offset` and sorts by label, because the offsets and
  their order follow the current date's DST rules in Django itself. The exact output, order included, is checked
  instead by `api.TestTimezoneList`. That test uses fixtures recorded from the reference with `datetime.now()`
  patched to 2026-01-15 and 2026-07-15, and Go's tzdata matched pytz 2024a on both dates.
- **No Redis page cache (changed at merge).** The agent ported `cache_page`'s Redis storage. The lead
  replaced it: Go builds the list per request and sends the same `Expires` and `Cache-Control: max-age=7200`,
  so browsers cache it as before. A Redis key per Host and query string, kept 2 h, is unbounded growth in the
  shared Redis. The scenario reads with a fresh query string whenever it records headers, so Django's reads are
  uncached too. `Expires` is recorded by shape only (`headerStep`), as it follows the wall clock.

## 4. Shared-file changes

- `internal/api/issue_list.go` (additive): adds an `issueList.workspace` flag. When it is set, `groupValues` drops
  the project filter (`issue_group_values` without `project_id`): workspace-wide states, labels, modules and
  cycles, workspace members for `assignees__id`, and no project filter on the date/creator values.
  Existing behaviour is unchanged.
- No changes to `issue_activity.go`, `issue_query.go` or the harness. The `headerStep` helper lives in
  `contract/misc_test.go`.

## 5. Test setup and open questions

- **Seeding.** Cycles and cycle issues are inserted with SQL through `s.DBStrings`, because cycle routes aren't
  ported yet. The same SQL also pins activity timestamps and epochs (so the CSV, dates and pages are
  reproducible), and nulls the `issue_id` of the "deleted" activity to mimic a hard-deleted issue.
- **Ordering masks.** `user-profile` `project_data` is a GROUP BY without ORDER BY, so it is compared Unordered.
- **IssueActivitySerializer.** It is ported locally (`issueActivityItem`). The issue-discussion port will
  probably need the same serializer, so the lead may want to merge the two. `source_data` is always null here:
  only the issue-history view sets that prefetch.
- **Known failure.** `TestIssueBulk` fails in this worktree on the date rollover (fixed by the lead in main);
  everything else is green.
