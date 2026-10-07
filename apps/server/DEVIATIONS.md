# Deliberate deviations from the Django API

The Go server reproduces the Django backend's responses exactly, as checked by
the contract goldens. These are the known, intentional differences.

| Area | Django | Go | Why |
|---|---|---|---|
| `GET /api/instances/` caching | Response cached in Redis for 2 h, so `workspaces_exist` stays stale after the first workspace is created | Computed per request | Stale cache was a bug; the query is cheap |
| `ENABLE_SIGNUP` default | `"0"` for the instance config the web app reads, `"1"` for the actual sign-up check | One setting, default `1` | One knob, one meaning |
| God-mode admin | Instance must be set up through the admin app before anyone can sign in | Instance registered and marked set up at boot; all config comes from env vars | Admin app dropped |
| Accepted project invites on login | `ProjectMember` rows created without `project_id`, which violates NOT NULL and fails the login | Rows include `project_id` | Bug fix |
| Login redirect | Computes `get_redirection_path()` ("onboarding", a slug, ...) and then discards it because it lacks a leading `/` | Skips the computation; same redirect (app root, or `next_path`) | Same behaviour, no wasted queries |
| Session payload | Django's signed, compressed `session_data` | Plain JSON in the same `sessions` table | Clean cutover; sessions don't need to be readable by Django. Existing sessions are invalidated on migration (users sign in again) |
| Background jobs | Celery on RabbitMQ, separate worker and beat processes; a failed task is logged and dropped | River queue in Postgres, worked inside the server process; email jobs retry up to 3 times | No broker to run; jobs survive Redis eviction |
| Malformed JSON bodies | 400 `{"detail": "JSON parse error - <Python json message>"}` | Same status and prefix; the message after the dash is Go's | Not worth reimplementing Python's parser messages |
| Lone UTF-16 surrogates in JSON (`"\ud800"`) | Accepted by the parser; CharFields answer "Surrogate characters are not allowed" | Rejected as malformed JSON (400) | Go strings can't hold lone surrogates |
| `/api/users/me/accounts/` | Lists the user's OAuth accounts | Always `[]`; `/accounts/<id>/` is always 404 | OAuth is cut, so no accounts can exist |
