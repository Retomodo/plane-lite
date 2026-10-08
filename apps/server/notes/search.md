# Search (PORTING.md section 16)

## 1. Endpoints ported and goldens

| Method | URL | Django view | Golden |
|---|---|---|---|
| GET | `W/search/` | `GlobalSearchEndpoint.get` | `search_global` (171 steps) |
| GET | `P/search-issues/` | `IssueSearchEndpoint.get` | `search_issues` (187 steps) |
| GET | `W/entity-search/` | `SearchEndpoint.get` | `search_entity` (192 steps) |

Files: `internal/api/search.go` (shared helpers), `search_global.go`, `search_issue.go`,
`search_entity.go`, `routes_search.go`, `contract/search_test.go`.

All three scenarios share `newSearchFixture`. It sets up `projectTeam`, plus:

- erin, removed from PL (her membership is inactive);
- robot, a bot (`is_bot` set with SQL);
- frank, who left the workspace;
- a second workspace;
- a secret project OT (alice and dan), a public project PU (alice only) and an archived project AR;
- issues covering every case the filters care about: parent and child, a relation and a removed
  relation, cycle and module membership (including removed links), target dates, deleted,
  archived, draft and triage issues, special characters and `12.5`;
- cycles in every status (one archived, one deleted);
- modules (one archived, one deleted, one issue moved between modules);
- views (one deleted, one at workspace level);
- pages: private, archived, deleted, one linked to two projects, one with a soft-deleted project
  link, and one global page.

The queries cover:

- every role, plus anonymous and unknown slugs;
- empty and whitespace queries, `%`, `_`, `\`, quotes and an injection-looking string;
- sequence ids: `1`, `PL-1`, `12.5`, `5 and 6`, `pl1`, Arabic-Indic `٣`, overflowing numbers, and
  queries over 20 characters;
- every filter flag the web sends (`parent`, `issue_relation`, `sub_issue`, `cycle`, `module`,
  `target_date`, `workspace_search`, `epic`) with valid, dead and malformed ids;
- every `query_type`, both workspace-wide and project-scoped;
- `count` values: default, 0, 2, negative, `abc`, ` 3 `, `1_0`, int64 max and int64 max + 1.

The SQL was taken from `scripts/capture-sql.sh` and ported join for join.

## 2. Not ported

Nothing in section 16 is left out. Only the `intake` bucket of `W/search/` is stubbed (see below).

## 3. Deviations

| Django | Go | Why |
|---|---|---|
| `W/search/` `intake` bucket lists intake issues (pending or snoozed) matching the query | Always `[]` | Intake is cut. With no intake data both answer `[]`, so the golden matches. A malformed `project_id` still answers 400 when only `intake` is requested, as in Django. |

No Redis caches are involved.

## 4. Changes to shared files

None. `routes_search.go` was already wired into `api.go`. The helpers I reused (`likeEscape`,
`issueObjects`, `errFilterDetail`, `errViewCrash`, `dateOrNil`, `drf.ParseUUID`,
`drf.PyIntString`, `drf.PyStrip`) are used unchanged.

## 5. Quirks ported (see the goldens)

### Global search (`W/search/`)

- Only `IsAuthenticated` is checked.
- The `workspace` bucket ignores the slug. It lists every workspace the user has any membership row
  in, active or not (frank, who left, still sees Acme).
- The other buckets need an active project membership and a project that isn't archived.
- Archived cycles, modules and pages are listed, and so are other users' private pages.
- `issue` is capped at 100 rows. The other buckets have no limit.
- `?entities=` is split on commas, stripped, and unknown names are dropped. An empty value means
  all buckets; a list with no known names gives `{"results": {}}`.
- `?project_id=` applies only when `workspace_search` is exactly `"false"` (the default). A
  malformed id gives 400 "Please provide valid detail" unless only `workspace`/`project` are
  requested.
- Pages:
  - `project_ids` and `project_identifiers` only aggregate over projects the user is an active
    member of.
  - The page join ignores `project_pages.deleted_at`, but the project filter's subquery checks
    it.
  - `ArrayAgg(filter=~Q(projects__id=True))` compares with `UUID(int=1)`, so it has no effect.

### Issue search (`P/search-issues/`)

- Only `IsAuthenticated` is checked. A non-member gets `[]`.
- A guest *of the URL's project* only sees issues they created, even with
  `workspace_search=true`. A guest searching through another project's URL sees everything.
- `search_issues`:
  - adds DISTINCT (and `created_at` to the SELECT) only when a query is given;
  - for queries over 20 characters, matches `sequence_id::text` with LIKE instead of whole
    numbers.
- Each flag needs exactly `"true"`, and the pickers also need `issue_id`.
- The picker issue is looked up with `issue_objects`, unscoped by project. A malformed id gives
  400, and a missing or archived one applies no exclusion.
- `sub_issue` with a missing issue gives 500 (`None.parent`).
- `module` excludes an issue that ever had a row for that module (even a deleted one) as long as
  it has any live module row. Django splits the multi-valued `exclude` into two `EXISTS`, so the
  "Moved modules" issue is excluded from its old module's picker. `cycle` is split the same way.
- Errors come in the view's order: `parent`, `issue_relation`, `sub_issue`, then `module`
  validation.
- `target_date` must be exactly `"none"`.

### Entity search (`W/entity-search/`)

- `WorkspaceUserPermission` is checked: an active, non-deleted workspace membership.
- `count` goes through `int()`:
  - a non-integer gives 500;
  - a negative count gives 500, but only once a known type runs;
  - 0 gives `[]` without a query;
  - a count past bigint fails in Postgres (500).
- A malformed `project_id` gives 400 for every type except `project`. Errors come in type order.
- `user_mention`:
  - workspace-wide, it lists active, non-bot workspace members (no DISTINCT);
  - project-scoped, it lists the project's active members with DISTINCT;
  - there is no check that the requester is in that project.
- `project` lists public projects (`network=2`) plus any project the user has a membership row in.
  The membership can be inactive (erin still sees PL), and archived projects are included.
- `issue` excludes archived projects only through `issue_objects`. `cycle` and `module` keep
  archived projects' and archived items.
- `page`:
  - only public pages are listed;
  - workspace-wide, only `is_global` pages appear, once per linked project the user is in
    (`projects__id`);
  - project-scoped, the link's `deleted_at` is ignored.
- `cycle.status` is computed against `now()`, which Django passes as `timezone.now()`.

## 6. Test notes and open questions

- The scenario writes some rows with SQL because no endpoint makes them:
  - the bot flag;
  - an avatar asset (file assets come later);
  - a triage-state issue (intake is cut);
  - carol as an issue's creator (guests can't create issues);
  - a second project link and a soft-deleted link for pages;
  - `pages.is_global`.
- Masks: `archived_at` on the cycle, module, page and project archive answers (wall clock).
- `Unordered` is used on `results.page[].project_ids` and `project_identifiers`, which are
  array_agg(DISTINCT uuid), so their order follows the random ids. It is also used on the
  workspace-wide entity-search `page` arrays. Those only hold the global page, once per project;
  the rows tie on `created_at`, and DISTINCT orders the tie by the random project ids. `SortBy`
  can't fix this because it sorts on the raw ids.
- Python's `\b\d+\b` is Unicode-aware. Go reimplements it in `searchSequenceIDs`: Unicode digits
  (Nd), with word characters being letters, numbers and `_`. Characters that Python counts as
  numeric outside the N categories are an untested edge case.
- Out-of-range sequence numbers (above int32) are dropped from the OR, as Django drops the lookup.
  The goldens only show that they match nothing; an int32-overflowing number that Django would turn
  into a DB error was not observed.
