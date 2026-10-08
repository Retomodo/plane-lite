# plane-lite roadmap

Handoff for anyone (or any session) picking up the Go rewrite of Plane's Django API. Read this
first, then the files it points to.

## Where things stand (2026-10-08, end of wave 3)

- **Ported and verified:** every used endpoint in PORTING.md (sections 1–18), including favorites,
  search and file assets, plus the event-driven and periodic jobs (River, in-process). Each endpoint
  has golden contract tests recorded against the Django reference. `go test ./... -count=1` is green
  without object storage (asset scenarios skip) and with it (`PLANE_DEV_S3=1`, MinIO).
- **Frontend trimmed:** `apps/web` no longer shows or calls the cut features (`notes/web-trim.md`).
- **Single image:** the root `Dockerfile` builds web, live and the Go server into one container on
  port 8000; Go serves the static build and proxies `/live/`. See [deploy/README.md](deploy/README.md)
  (env template and nginx block). Verified in a headless browser against the image: magic-code
  sign-up, onboarding, workspace, project with cover upload, work item, comment, and a page edited
  over the live websocket and reloaded.
- **Deliberate differences:** all listed in [DEVIATIONS.md](DEVIATIONS.md).
- **Per-area detail** (quirks kept, test choices, open questions): `notes/<area>.md`.

## How the work is done

- [contract/PLAYBOOK.md](contract/PLAYBOOK.md) is the porting procedure: record a scenario against
  Django, implement in Go, verify, and the rules for test masks.
- [PORTING.md](PORTING.md) lists every endpoint with its Django view, serializer, permission and side
  effects. Ported rows are ticked ☑; unticked rows are either still to do or **UNUSED** (no caller).
- Parallel porting: one agent per area, each in a git worktree (`../plane-lite-worktrees/<area>`,
  branch `port/<area>`, from the latest commit) with its own Docker stack slot
  (`scripts/devstack.sh N up`, `CONTRACT_SLOT=N`). Slot 0 is for the lead. The lead reviews each
  branch, applies it to the main tree uncommitted, folds `notes/<area>.md` into PORTING, DEVIATIONS
  and README, and runs the full suite. The user makes all commits on `preview`.
- Model choice: Opus for areas with complex queries or side effects, Sonnet for plain CRUD. Haiku is
  not used.

## What's left, in order

### 1. First deployment

- Build the image from the repo root, fill in `deploy/plane-lite.env.example`, put the nginx block in
  front, and point `DATABASE_URL`, `REDIS_URL` and the R2 `AWS_*` settings at the real services.
- Run the asset flow once against the real R2 bucket (`CONTRACT_S3_*` lets the Go-side asset
  scenarios run against it); nothing has touched R2 yet.
- The image is about 1 GB, mostly live's runtime dependencies (editor, PDF export). Slimming it is
  optional.

### 2. Frontend follow-ups

- React reports a hydration warning (#418) on every load: React Router's SPA mode hydrates its
  prerendered shell. Cosmetic; check whether upstream has it too.
- The new work-item modal asks before discarding only once the title or description has content
  (as the old draft prompt did).
- The cut code still lives in the shared packages (`packages/services`, `constants`, `types`, i18n)
  because admin and space build against them. Remove it when those apps go.

### 3. Cutover

- Schema: drop the cut tables (intake, deploy boards, webhooks, API tokens, …) once nothing reads
  them. Then regenerate `internal/softdelete/relations_gen.go` (`contract/reference/gen_cascade.py`)
  and remove the `intake_count: 0` / `anchor: null` stubs.
- Data: the Go schema is Django migration 0122 (`internal/db/0001_baseline.sql`), so an existing Plane
  database can be pointed at directly. Sessions are invalidated (users sign in again).
- Retire `apps/api`. Until then it is the reference that records goldens.

### 4. Open follow-ups

- `cycle_crud` runs its project on Asia/Kolkata and was only recorded and verified while the UTC and
  Kolkata dates matched. If it ever fails only between 18:30 and 24:00 UTC, that's the cause; fix the
  scenario rather than masking it.
- The module and cycle burndowns are only recorded for UTC users. The user-timezone path is ported
  but not exercised.
- The `misc` and `view` scenarios seed cycles and modules with SQL from before those were ported, and
  the cycle, module, view and page scenarios seed `user_favorites` the same way. They could use the
  API now; this is optional.
- The old `port/*` branches can be deleted once their work is committed.
