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
| Workspace creation | Enqueues `workspace_seed`: a bot user and a demo project with states, labels, issues, cycles, modules, pages and views | Creates only the workspace and the creator's membership (the contract reference has the task disabled) | Demo data is clutter for a team that brings its own work |
| Unused workspace endpoints | `W/workspace-themes/…` and `/api/users/last-visited-workspace/` answer (the latter with a 500 in the recorded reference) | 404 | No caller in the web or live apps |
| Project payloads and CUT features | `W/projects/` counts pending intake issues as `intake_count`; project details carry the deploy-board `anchor`; turning `inbox_view` on creates a default Intake | `intake_count` is always `0` and `anchor` always `null`; the `intake_view` flag is stored but no Intake row is created | Intake and publishing are cut |
| Project activity tasks | Project create, update and delete enqueue `model_activity` / `webhook_activity` | Not run | They only feed webhooks, which are cut |
| Unused project endpoints | `PUT W/projects/<pk>/`, `DELETE W/project-identifiers/`, `GET /api/users/me/workspaces/<slug>/projects/invitations/` answer; so do `P/invitations/…`, `P/join/<pk>/`, `P/project-views/` and `P/preferences/member/<id>/` | 405 for the first three (their URLs have other methods), 404 for the rest | No caller in the web or live apps. Project invitations can't be created anyway (Django's create crashes); people join projects through workspace invites or the project's member list |
| `GET W/labels/` caching | Cached per user in Redis for 2 h. Creating a label clears it; editing or deleting one, archiving a project or changing members does not, so the list goes stale | Computed per request | Stale cache was a bug; the query is cheap |
| Unused label endpoints | `GET`/`PUT P/issue-labels/<pk>/` and `POST P/bulk-create-labels/` answer | 405 for the first two (the URL has PATCH and DELETE), 404 for bulk create | No caller in the web or live apps |
| HTML sanitizer | nh3 (Rust ammonia on html5ever) | A Go port on a vendored copy of `x/net/html`, checked against nh3 output for the editor's markup, adversarial cases and 3,184 random fragments | No Rust in the build. 18 random tag-soup fragments (misnested formatting elements inside tables and `<select>`) still clean differently; editor output and every hand-written case match |
| Issue `?expand=` | Any relation `IssueDetailSerializer` can expand | `issue_reactions`, `issue_attachments`, `issue_link` and `parent`, the ones the web asks for; other names are ignored | No other caller |
| Issue origin cache | `issue_activity` stores the request's origin in Redis per issue, for links in notification emails | Not written; emails build links from the configured app URL | The origin is configuration, not per-request state |
| Unused issue endpoints | `GET P/issues-detail/`, `GET P/v2/issues/`, `PUT P/issues/<pk>/` and `GET P/deleted-issues/` answer | 405 for the `PUT` (the URL has other methods), 404 for the rest | No caller: the web only asks for `issues-detail` with `expand=issue_relation`, which it never sends; the other service functions are dead |
| `P/issues/list/` `?fields=` / `?expand=` | Switch the response to `IssueSerializer` with those fields and expansions | Ignored: always the plain `.values()` list | The web sends only `?issues=` |
