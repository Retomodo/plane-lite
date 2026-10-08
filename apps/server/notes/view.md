# Saved views (PORTING.md section 11)

## 1. Endpoints ported and goldens

| Method | URL | Django view | Golden |
|---|---|---|---|
| GET, POST | `P/views/` | `IssueViewViewSet.list` / `.create` | `views` (156 steps) |
| GET, PATCH, DELETE | `P/views/<pk>/` | `IssueViewViewSet.retrieve` / `.partial_update` / `.destroy` | `views` |
| GET, POST | `W/views/` | `WorkspaceViewViewSet.list` / `.create` | `views` |
| GET, PATCH, DELETE | `W/views/<pk>/` | `WorkspaceViewViewSet.retrieve` / `.partial_update` / `.destroy` | `views` |
| GET | `W/issues/` | `WorkspaceViewIssuesViewSet.list` | `workspace_view_issues` (134 steps) |

Files: `internal/api/view.go`, `internal/api/view_issues.go`, `internal/api/routes_view.go`,
`contract/view_test.go`. `views` ends each half with `viewRows` (issue_views, user_favorites,
user_recent_visits).

## 2. Not ported

- `PUT P/views/<pk>/` and `PUT W/views/<pk>/`: UNUSED (they answer 405).
- The `user-favorite-views/` routes are not in this section's list (favorites come in a later wave).

## 3. Deviations

None. There is no response cache on these views. `gzip_page` on `W/issues/` only changes the
transfer encoding.

## 4. Changes to shared files

- `internal/api/issue_list.go`: new `issueList.idArraySQL` field. When it is set, `values()` uses
  its `assignee_ids`/`label_ids`/`module_ids` expressions. `W/issues/` serializes prefetched
  relations: every live row, newest first, with no archived-module or label filter and no DISTINCT.
- `internal/api/issue_write.go`: the label validation query now has `ORDER BY created_at DESC`
  (Label's Meta ordering, which Django's `Label.objects.filter(...).values_list` applies). It fixes
  the order in which issue create/update inserts `issue_labels`, which `W/issues/` shows. All other
  goldens still pass.

## 5. Quirks ported (see the goldens)

- Create has no role check (`IsAuthenticated`): any signed-in user, even an outsider, can create a
  view in any project by id (the slug is ignored, and the workspace comes from the project) or in
  any workspace. An unknown project gives 404 after the filters are evaluated; an unknown workspace
  gives 404 before.
- `query` is `issue_filters(filters, "POST")`, recomputed on every save. This port includes its
  quirks: a string `filters` is a substring test, so a string containing a filter name gives 500, as
  does a list containing a filter name; a number gives 500; `len()` of a number gives 500; a string
  date value is iterated per character; `intake_status` copies `inbox_status`. A relative date such
  as `2_weeks;after;fromnow` puts a `date` in the JSONField, so the save gives 500.
- `deleted_at`, `archived_at` and `created_by` are writable. On create, `created_by` becomes the
  requester; on PATCH, the given `created_by` is stored. A new view's `sort_order` is 10000 above its
  live siblings' highest.
- Retrieve of a view that isn't visible renders `IssueViewSerializer(None).data` (the initial values
  of the writable fields) and still records a visit. For a guest limited by
  `guest_view_all_features`, it gives 500 instead. `W/views/<pk>/` GET has no role check.
- PATCH/DELETE use `allow_permission([] / [ADMIN], creator=True)`. At project level, a workspace
  admin who is a member of the project passes even with `[]`. `W/views/<pk>/` PATCH/DELETE also
  reach project views.
- Project DELETE soft-deletes every user's favorites of the view and hard-deletes visits to it.
  Workspace DELETE only soft-deletes favorites with no project.
- `W/issues/` ignores `group_by`/`sub_group_by` (always a flat page). Its project-membership join and
  relation filters are not DISTINCT, so an issue that matches several relation rows appears more
  than once and counts more than once. `total_count` counts the duplicates; an aggregate `order_by`
  (`labels__name`, ...) groups them back into one row.

## 6. Mask, Unordered, SortBy and SQL writes in tests

- `Unordered("*.assignee_ids", "assignee_ids", "label_ids")` in `workspace_view_issues`. The issue
  create path validates assignees with an unordered ProjectMember query, so their insertion order
  (and so the prefetch order) follows the query plan. The create response's arrays are
  `array_agg(DISTINCT)`, which sorts them by random id.
- SQL writes:
  - `user_favorites` INSERTs (`favoriteView`): the favorites endpoints come later.
  - `UPDATE issue_views SET access = 0` / `is_locked = true`: both fields are read-only in the
    serializer.
  - `modules`/`module_issues`/`cycles`/`cycle_issues` INSERTs, and one soft-deleted
    `module_issues` row: the module and cycle endpoints belong to other batches.
- The only `Mask` is the existing `Mask("archived_at")` on project archive (today's date).

## 7. Unsure

- `IssueViewSerializer(None).data` is rendered as a fixed dict. Key order follows DRF's field order,
  but the harness doesn't compare key order.
