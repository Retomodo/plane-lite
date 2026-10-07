# plane-lite server

A Go reimplementation of Plane's Django API (`apps/api`). One binary serves HTTP,
runs background jobs and owns schema migrations. It talks to an external
Postgres and Redis, configured through `DATABASE_URL` and `REDIS_URL`.

## Layout

| Path | What |
|---|---|
| `cmd/plane` | Entrypoint: `plane serve` (migrate, then serve) and `plane migrate` |
| `internal/api` | Endpoint handlers; each names the Django view it ports |
| `internal/auth` | Django-compatible passwords, sessions, CSRF and email validation |
| `internal/httpx` | Router, DRF-style errors, and JSON rendered in the user's timezone |
| `internal/db` | Pool and migrations. `0001_baseline.sql` is Django's schema; `0002` adds the ORM's defaults at the DB level |
| `internal/throttle` | DRF throttling on Redis |
| `contract` | Golden-file contract tests against the Django reference |

## Contract tests

Every ported endpoint is covered by a scenario in `contract/*_test.go`. A
scenario is recorded once against Django, and the Go server must reproduce
it. IDs, timestamps and tokens are normalized, but datetime offsets are kept
because Plane renders times in the user's timezone.

```sh
make test                      # unit + contract tests against Go (starts db/redis/mail)
make record RUN=TestAuthSignUp # re-record goldens from the Django reference
```

Porting loop for each batch:

1. Write the scenario.
2. Run `make record` and review the golden diff.
3. Implement the endpoint in Go.
4. Run `make test` until it's green.

Intentional differences from Django are listed in [DEVIATIONS.md](DEVIATIONS.md).

## Configuration

Variables use the Django backend's names:

- **Required:** `SECRET_KEY`, `DATABASE_URL`, `REDIS_URL`.
- **Common:**
  - `REDIS_KEY_PREFIX` (default `plane:`; keeps a shared Redis safe)
  - `DATABASE_MAX_CONNS` (default 10)
  - `APP_BASE_URL`, `WEB_URL`, `CORS_ALLOWED_ORIGINS`
  - `ENABLE_SIGNUP`, `ENABLE_EMAIL_PASSWORD`, `ENABLE_MAGIC_LINK_LOGIN`
  - `EMAIL_HOST`, `EMAIL_PORT`, `EMAIL_HOST_USER`, `EMAIL_HOST_PASSWORD`, `EMAIL_USE_TLS`, `EMAIL_FROM`

## Porting status

| Batch | Endpoints | Status |
|---|---|---|
| 1 | Routing conventions (slash redirect, 404, 405), `GET /api/instances/`, `/auth/get-csrf-token/`, `/auth/email-check/`, `/auth/sign-up/`, `/auth/sign-in/`, `/auth/sign-out/`, `GET /api/users/me/` | done |
