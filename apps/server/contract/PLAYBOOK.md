# Porting playbook

How to port one area of the Django API (`apps/api`) to the Go server (`apps/server`). Read it all
before starting. `PORTING.md` lists the endpoints per area, `DEVIATIONS.md` the deliberate
differences, and batches 5 to 8 (workspaces, projects, states/labels, issues) are worked examples
to copy from.

## The goal

The Go server must answer **exactly** as Django does: the same status, the same body, the same rows
written (including `created_by`, `updated_by`, soft deletes and activity rows) and the same emails.
"Exactly" includes Django's bugs and quirks: a 500 where Django crashes, a duplicate row where
Django's joins duplicate it, a stale field Django forgets to update. Port them; don't fix them.

The only allowed differences are:

- endpoints no web or live code calls (marked **UNUSED** in `PORTING.md`), which stay unported;
- cut features (intake, publishing, analytics, webhooks, API tokens, OAuth, exporter, importers, AI,
  drafts, telemetry), stubbed as the area's notes say;
- cases where Python and Go genuinely can't behave the same (rare; say why).

Every difference goes in your notes file (below), with the reason.

## Your setup

You are given an **area** (one `internal/api/routes_<area>.go` file) and a **slot** number. You work
in your own git worktree.

```sh
cd apps/server
scripts/devstack.sh $SLOT up        # your own Postgres, Redis, Mailpit and Django reference
export CONTRACT_SLOT=$SLOT          # every go test below uses your stack
```

Never use another slot, and never run tests without `CONTRACT_SLOT`: slot 0 belongs to the lead.
Run `scripts/devstack.sh $SLOT down` when you are finished.

Useful commands:

- `scripts/devstack.sh $SLOT psql plane_ref` (Django's data after a recording) or `psql` (Go's).
- `scripts/devstack.sh $SLOT shell < script.py`: a Django shell on the reference.
- `scripts/capture-sql.sh $SLOT requests.txt`: the SQL Django runs for some requests. Use it whenever
  a view joins multi-valued relations, annotates, groups or paginates. The ORM's join reuse decides
  which rows exist, and only the SQL shows it.

## The loop

1. **Read the Django code first**: the URL conf, the view, its permission classes and
   `@allow_permission` decorator, the serializers, the model's `save()`/manager, and every task the
   view calls (`.delay()` runs inline in the reference, so its effects are part of the response's
   side effects). Read `apps/api/plane/...` directly; don't guess from names.
2. **Write the scenario** in `contract/<area>_test.go` (one or a few `Run(t, "<golden>", ...)`). Build
   on the fixtures other scenarios use (`projectTeam`, `signUp`, `createWorkspace`, `userID`,
   `findBy`, `idOf`, `mention`). Cover:
   - every role: `projectTeam` gives alice (workspace and project admin), bob (member), carol
     (guest), dan (workspace member, not in the project) and outsider (no workspace);
   - validation errors, missing and unknown ids (`dead`), wrong body types (a list body, a string
     where a list goes), deleted and archived objects;
   - the side effects: `s.DBRows` over every table the view writes, selecting names and emails
     rather than ids, and comparisons rather than raw timestamps (see `issueRows`).
3. **Record** against Django and read the golden (`contract/testdata/golden/<name>.json`):
   `CONTRACT_RECORD=1 go test ./contract/ -run TestX -count=1`. Understand every surprise before
   writing Go.
4. **Implement** in Go, then verify: `go test ./contract/ -run TestX -count=1`. Iterate until green.
5. **Check stability**: `go test ./contract/ -run TestX -count=5`, then `gofmt -l .`,
   `go vet ./...` and the whole suite, `go test ./... -count=1`.

Changing a scenario means recording it again. Recording is deterministic: an unchanged scenario
records a byte-identical golden.

## Rules for tests (these are checked at review)

- **Never edit a golden by hand.** Goldens only come from recording.
- **Don't loosen the comparison to get green.** `Mask`, `Unordered` and `SortBy` are only for values
  that vary *in Django itself* from run to run (random ids deciding an order, tie order between
  rows, tokens). Each use needs a comment saying where the variation comes from.
- Values that depend on the day the scenario runs (today's date in `archived_at`, cycle status
  relative to now) must not end up literal in a golden, or it breaks the next day. Use
  `s.AliasToday()`, or build input dates relative to today and `s.Alias` them.
- Don't change other areas' scenarios or goldens. Don't weaken the harness.
- If a mismatch still makes no sense after three honest attempts, **stop and report** what you see
  (the step, the diff, what you tried) instead of masking it.

## Writing the Go

Your files: `internal/api/routes_<area>.go` (routes and job registration, both already wired into
`api.go`), any `internal/api/<area>*.go` you add, `contract/<area>_test.go` and your goldens. Avoid
editing other files. If you must change shared code (`internal/drf`, `internal/httpx`, the harness,
a helper from another batch), keep the change small and additive, and list it in your notes.

Conventions:

- Each handler's comment names the Django view it ports (`// listCycles ports CycleViewSet.list.`).
  Comment density, naming and idiom follow the existing files.
- Other agents write code in the same Go package at the same time. Before defining a type or helper,
  grep for an existing one (serializer ports like `userLite`, `issueFlat`, `issueActivityOut` and
  `detailLoader` are shared), and give new package-level names an area prefix (`cycleX`, `errCycleX`)
  so parallel ports can't clash.
- Don't port Redis response caches (`cache_response`, `cache_page`): Go computes per request (see
  DEVIATIONS.md). Give the scenario's reads fresh query strings so Django serves them uncached too.
- When a scenario needs rows whose endpoints aren't ported yet (favorites, cycles), write them with SQL
  through `s.DBStrings` (an INSERT ... RETURNING); it writes identically in both modes. Say so in a
  comment.
- URL prefixes: `wsPrefix` (`W/`) and `projectPrefix` (`P/`); see the existing routes in `api.go`.

Reusable pieces:

- **Permissions:**
  - `allowProject(roles, h)` / `allowWorkspace(roles, h)` port `@allow_permission` at project or
    workspace level.
  - `workspacePerm`, `projectBasePerm` and `projectEntityPerm` port the DRF permission classes;
    a class runs before the decorator, so wrap the class around the decorator.
  - `allowIssue` is `creator=True` for issues.
  - The role sets are `anyRole`, `adminMember` and `adminOnly`.
- **Errors:** return `pgx.ErrNoRows` for `Model.objects.get` misses (generic 404); `errViewCrash` for
  a Django 500. `httpx.Err(status, msg)` gives `{"error": msg}`, `httpx.Detail` `{"detail": msg}`
  and `httpx.Body` any body. Other existing errors: `errNoRole`, `errFilterDetail` ("Please provide
  valid detail", Django's `ValidationError` from a bad lookup) and `errKeyMissing` (`KeyError`).
- **Request parsing:**
  - `drf.Parse(c.R)` gives `request.data`.
  - `drf.NewValidator` ports DRF fields: `Char`, `Int`, `IntNull`, `Bool`, `Float`, `Choice`,
    `UUID`, `PK`, `PKList`, `Date`, `DateTime` and `JSON`, plus `Require` and `Err()`.
  - `c.Query`, `c.HasQuery` and `c.QueryValues` read `request.GET` (last value wins, `;` kept).
- **Responses:**
  - `c.JSON` renders `time.Time` in the user's timezone, as serializers do.
  - `utcTime` renders UTC (for `.values()` rows); `httpx.Date` renders `YYYY-MM-DD`.
  - Counts from `Subquery(... .annotate(count=Count()))` are NULL when zero (`nullIfZero`).
- **Writes:**
  - BaseModel `save()` sets `created_by` to the request user on create (`updated_by` stays NULL)
    and `updated_by` on update; `bulk_create`/`bulk_update`/queryset `update()` set neither.
  - `bulk_create` is one INSERT, so all or nothing.
  - Use `clock_timestamp()` where several rows in one statement must keep their order.
  - `softdelete.Row` is `instance.delete()` with its cascade; queryset `.delete()` is a plain
    UPDATE with no cascade.
- **Jobs:** Celery tasks become jobs: `jobs.Register` in your `register<Area>Jobs` and
  `jobs.Enqueue`. They run inline in tests (as `.delay()` does in the reference).
  - `a.enqueueIssueActivity(ctx, issueActivityJob{...})` is `issue_activity.delay(...)`. It already
    handles the issue events and notifications; extend its type switch for yours
    (`comment.activity.*`, `cycle.activity.*`, and so on) and say so in your notes.
- **Issue lists:** `issueQuery` and `issueList` (`issue_query.go`, `issue_list.go`) port the issue
  filters, ordering, grouping and paginators. Cycle, module and view lists should reuse them rather
  than rebuild them.
- **Other helpers:**
  - `sanitize.HTML` is `validate_html_content` (nh3); `sanitize.Mentions` extracts mentions.
  - `recordVisit` is `recent_visited_task`.
  - `parseOffsetPage`/`pageResponse` give the plain offset paginator; `sanitizeOrderBy` ports
    `sanitize_order_by`.

## What you hand back

Write `apps/server/notes/<area>.md` with:

1. the endpoints ported (method and path as in `PORTING.md`) and the goldens covering them;
2. the endpoints not ported and why (UNUSED, cut);
3. deviations, each with Django's behaviour, Go's and the reason;
4. changes to shared files;
5. anything you were unsure about.

Don't edit `PORTING.md`, `DEVIATIONS.md` or `README.md`; the lead folds your notes in when merging.
Commit your work on your worktree branch when the whole suite is green.
