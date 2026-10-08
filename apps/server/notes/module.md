# Modules (PORTING.md section 9)

## 1. Endpoints ported

| Method | URL | Django view | Golden |
|---|---|---|---|
| GET | `P/modules/` | `ModuleViewSet.list` | `modules` |
| POST | `P/modules/` | `ModuleViewSet.create` | `modules` |
| GET | `P/modules/<pk>/` | `ModuleViewSet.retrieve` (+ `recent_visited_task`) | `modules`, `module_links` |
| PATCH | `P/modules/<pk>/` | `ModuleViewSet.partial_update` | `modules`, `module_archive` |
| DELETE | `P/modules/<pk>/` | `ModuleViewSet.destroy` | `modules`, `module_issues` |
| POST | `P/issues/<issue_id>/modules/` | `ModuleIssueViewSet.create_issue_modules` | `module_issues` |
| POST | `P/modules/<module_id>/issues/` | `ModuleIssueViewSet.create_module_issues` | `module_issues` |
| DELETE | `P/modules/<module_id>/issues/<issue_id>/` | `ModuleIssueViewSet.destroy` | `module_issues` |
| POST | `P/modules/<module_id>/module-links/` | `ModuleLinkViewSet.create` | `module_links` |
| PATCH | `P/modules/<module_id>/module-links/<pk>/` | `ModuleLinkViewSet.partial_update` | `module_links` |
| DELETE | `P/modules/<module_id>/module-links/<pk>/` | `ModuleLinkViewSet.destroy` | `module_links` |
| GET | `P/modules/<module_id>/user-properties/` | `ModuleUserPropertiesEndpoint.get` | `modules` |
| PATCH | `P/modules/<module_id>/user-properties/` | `ModuleUserPropertiesEndpoint.patch` | `modules` |
| GET | `P/archived-modules/`, `P/archived-modules/<pk>/` | `ModuleArchiveUnarchiveEndpoint.get` | `module_archive` |
| POST | `P/modules/<module_id>/archive/` | `ModuleArchiveUnarchiveEndpoint.post` | `module_archive` |
| DELETE | `P/modules/<module_id>/archive/` | `ModuleArchiveUnarchiveEndpoint.delete` | `module_archive` |
| GET | `W/modules/` | `WorkspaceModulesEndpoint.get` | `modules`, `module_issues`, `module_archive` |

Goldens (in `contract/module_test.go`): `modules` (140 steps), `module_issues` (108), `module_links` (73),
`module_archive` (90). They cover every role, list and wrong-type bodies, dead ids, archived and deleted
modules, archived/draft/cross-project issues and a points estimate. They also record every module table
(`moduleRows`: modules, members, module issues, links, user properties, module favorites, module recent
visits) and everything `issueRows` records. Retrieve and the archived detail record the distributions and
both burndown charts. Their dates are built relative to today and aliased (`moduleDay`).

Files: `internal/api/module.go` (shared annotated query builder, list/create/patch/delete/retrieve,
workspace list), `module_detail.go` (ModuleDetailSerializer, distributions, `burndown_plot`),
`module_props.go`, `module_issue.go`, `module_link.go`, `module_archive.go`, `issue_activity_module.go`
and `routes_module.go`.

The shared query: `moduleAnnotatedSQL(archived, ...)` is both `ModuleViewSet.get_queryset` and the copy in
`ModuleArchiveUnarchiveEndpoint.get_queryset`. It computes is_favorite, the six issue counts and the six
estimate sums, from SQL captured with `scripts/capture-sql.sh`. The views pick their shapes from one
`moduleRow`: `.values()` for list/create/patch, `ModuleSerializer` for `?fields=`, `ModuleDetailSerializer`,
and the archived `.values()`. The `members` join only feeds `ArrayAgg`, so it is a correlated subquery.
That is equivalent: nothing else reads the join. Quirks kept:

- the archive copy's `member_ids` keeps soft-deleted ModuleMember rows;
- the archive detail's points label distribution drops the `issue_module__deleted_at` filter;
- the distributions join `issue_assignees`/`issue_labels` without `deleted_at`, so removed assignees and
  labels still count;
- the archived detail of a pk that is not an archived module answers 200 with
  `{"member_ids": [], ...}`, the data of `ModuleDetailSerializer(None)`;
- `W/modules/` counts module_issues joined to issues and states (no IssueManager), through an
  unfiltered `project_members` join;
- `TruncDate` reads `completed_at` in the user's timezone, while "future" days compare against the UTC
  date.

## 2. Not ported

All **UNUSED** by web/live. The routes exist for other methods, so these answer 405:

- `PUT P/modules/<pk>/`;
- `GET P/modules/<module_id>/issues/` (`ModuleIssueViewSet.list`; the web lists module issues through
  `P/issues/?module=`);
- `GET`/`PUT`/`PATCH P/modules/<module_id>/issues/<issue_id>/`;
- `GET P/modules/<module_id>/module-links/` and `GET`/`PUT .../module-links/<pk>/`;
- the "(unused)" route variants: `GET P/modules/<module_id>/archive/`, and `POST`/`DELETE` on
  `P/archived-modules/[<pk>/]`. All of them raise `TypeError` (500) in Django, because the kwarg names don't
  match the method signatures.

`model_activity` (create, patch) is not called: it only feeds webhooks, which are cut. The
`user-favorite-modules/` routes belong to the favorites area.

## 3. Deviations and notes

- No response caches are involved in this area.
- `ModuleLinkViewSet.create` is DRF's `CreateModelMixin`, which sets `Location` from the serializer's
  `url` field (`URL_FIELD_NAME`). The link's URL becomes a Location header, and Go sends it too.
- `ModuleLinkSerializer.update` runs `validate_url(validated_data.get("url"))`, so a PATCH without a `url`
  is always 400 `{"error": "Invalid URL format."}`. Ported as is (the web always sends the url).
- Activity: module-issue writes send `module.activity.created/deleted`. The trackers are in
  `issue_activity_module.go`. `module_id` goes through UUIDField as the trackers' identifier would, so a
  value it rejects (e.g. `str(None)` from `removed_modules: [null]`) ends the task with no row, as the
  failed `bulk_create` does in Django. Module events send no notifications (the existing early return).

## 4. Shared files changed

- `internal/api/issue_activity.go`: one `case "module.activity.created", "module.activity.deleted":` line
  (plus its call) in the `issueActivity` dispatcher.

## 5. Unsure / unverified

- The burndown's `TruncDate` in a non-UTC timezone is ported (`AT TIME ZONE` the user's zone) but not
  recorded. Whether a completion falls on "today" then depends on the time of day the scenario runs, which
  would make the golden unstable. Every chart in the goldens is read by a UTC user.
- Ties in the distributions' `ORDER BY first_name, last_name` (two assignees with the same names) come out
  in the plan's order on both sides. The scenarios give users distinct first names.
- `create_issue_modules` with a non-string module id (an int or bool, which UUIDField accepts) is ported from
  reading the code (`json.dumps` keeps the raw value) but not recorded.
