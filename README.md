# plane-lite

A slimmed-down fork of [Plane](https://github.com/makeplane/plane), the open-source project
management tool, built to be cheap to self-host.

Upstream Plane runs about a dozen containers: a Django API, Celery workers and beat, RabbitMQ,
Postgres, Redis, MinIO, a proxy, and separate web, admin, space and live apps. plane-lite replaces
the Django backend with a single Go server that answers the same API. Everything ships as **one
container** that idles at about 180 MB of memory. Postgres, Redis and object storage are external
services you point it at.

The web app is Plane's own, trimmed of the features this fork leaves out (see
[What's left out](#whats-left-out)).

## How it fits together

```
                 ┌──────────────────── plane-lite container (:8000) ────────────────────┐
browser ──nginx──┤ Go server (apps/server)                                              │
   (TLS)         │   /api/, /auth/   → API (a port of Plane's Django API)               │
                 │   /live/          → proxied to apps/live (Node, page collaboration)  │
                 │   everything else → the web app's static build (apps/web)            │
                 │   background and scheduled jobs → River, a queue in Postgres         │
                 └──────────┬───────────────────────┬───────────────────────┬───────────┘
                         Postgres           Redis (may be shared)   S3 storage (R2, S3, MinIO)
```

| Path | What it is |
|---|---|
| `apps/server` | The Go API server. It also serves the web build, proxies `/live/` and runs the background jobs |
| `apps/web` | Plane's web app (React), built to static files |
| `apps/live` | The real-time collaboration server for pages (Hocuspocus, Node) |
| `packages/*` | Shared TypeScript packages used by web and live |
| `Dockerfile` | Builds all three into the one image |

There's no separate worker, scheduler or message broker. Jobs Plane ran on Celery, such as
notification emails, activity history and nightly cleanups, run inside the Go process on a queue
stored in Postgres. The container needs to stay running for scheduled jobs to fire. A run missed
while the container was down executes once at the next start.

## Running it in the cloud

You need:

- **A small VM or container host.** The container uses about 180 MB once running and peaks at about
  400 MB during startup (Node loading the editor). Give it **512 MB** for comfort. It runs in
  **384 MB** if you cap Node's heap with `NODE_OPTIONS=--max-old-space-size=160`, which is what the
  numbers above were measured with. The Go server itself stays around 40 MB; almost all of the
  memory is the collaboration server.
- **Postgres** (tested on 15). Any managed Postgres works. The schema is created on first start.
- **Redis or Valkey** (tested on Valkey 7.2). Used for rate limits, sign-in codes and
  collaboration pub/sub; jobs don't live there, so eviction loses nothing important. It can be a
  shared instance: every key and channel starts with `REDIS_KEY_PREFIX` (default `plane:`).
- **S3-compatible object storage**, e.g. Cloudflare R2 (see [Storage](#storage-minio-vs-s3-or-r2)).
- **SMTP** for sign-in codes, invitations and notification emails.
- **A reverse proxy for TLS**, e.g. nginx or Caddy.

### 1. Build the image

```sh
docker build -t plane-lite .
```

The build needs about 4 GB of memory (the web app's Vite build). If your server is small, build
elsewhere, e.g. locally or in CI, and push the image to a registry.

### 2. Configure

Copy [`apps/server/deploy/plane-lite.env.example`](apps/server/deploy/plane-lite.env.example) to
`plane-lite.env` and fill it in. The essentials:

| Variable | Example | Notes |
|---|---|---|
| `SECRET_KEY` | 50+ random characters | Signs sessions, CSRF and reset tokens |
| `DATABASE_URL` | `postgres://user:pass@host:5432/plane?sslmode=require` | |
| `DATABASE_MAX_CONNS` | `10` | Keep under your Postgres plan's limit |
| `REDIS_URL` | `rediss://default:pass@host:6379/0` | `rediss://` for TLS |
| `REDIS_KEY_PREFIX` | `plane:` | Keeps a shared Redis safe |
| `WEB_URL`, `APP_BASE_URL`, `LIVE_BASE_URL` | `https://plane.example.com` | The public URL |
| `CORS_ALLOWED_ORIGINS` | `https://plane.example.com` | An `https` origin makes cookies `Secure` |
| `AWS_S3_ENDPOINT_URL`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_S3_BUCKET_NAME`, `AWS_REGION` | see [Storage](#storage-minio-vs-s3-or-r2) | |
| `EMAIL_HOST`, `EMAIL_PORT`, `EMAIL_HOST_USER`, `EMAIL_HOST_PASSWORD`, `EMAIL_USE_TLS`, `EMAIL_FROM` | | SMTP |
| `ENABLE_SIGNUP`, `ENABLE_EMAIL_PASSWORD`, `ENABLE_MAGIC_LINK_LOGIN` | `1` | Sign-in options (the admin app is gone; config is env only) |
| `NODE_OPTIONS` | `--max-old-space-size=160` | Optional: caps the collaboration server's heap on small hosts |

The full list is in [`apps/server/README.md`](apps/server/README.md#configuration).

### 3. Run

```sh
docker run -d --name plane-lite --restart unless-stopped \
  --env-file plane-lite.env -p 127.0.0.1:8000:8000 plane-lite
```

On start the container applies database migrations, then starts the collaboration server and the
API. If either process dies, the container exits and the restart policy brings it back. Point your
reverse proxy at port 8000. It must pass websockets through for `/live/` and allow uploads at least
as large as `FILE_SIZE_LIMIT` (5 MB by default). A complete nginx server block (TLS, gzip,
websockets, upload size) is in [`apps/server/deploy/README.md`](apps/server/deploy/README.md#nginx).

Coming from an existing Plane install? plane-lite uses Plane's database schema as of Django
migration 0122, so you can point it at that database directly. Everyone has to sign in again.

## Running it locally

You need Docker. The dev compose file in `apps/server` provides throwaway Postgres, Redis, a mail
catcher (Mailpit) and, optionally, MinIO:

```sh
cd apps/server
docker compose -f docker-compose.dev.yml up -d --wait db redis mail
docker compose -f docker-compose.dev.yml --profile s3 up -d --wait minio   # only for file uploads
```

The database lives in memory and is wiped when its container stops.

Build and run the image against them:

```sh
docker build -t plane-lite .            # from the repo root
docker run --rm --network host \
  -e PORT=8000 -e SECRET_KEY=local-dev-secret-key-change-me-0123456789 \
  -e DATABASE_URL=postgres://plane:plane@127.0.0.1:55432/plane \
  -e REDIS_URL=redis://127.0.0.1:56379/0 \
  -e WEB_URL=http://localhost:8000 -e APP_BASE_URL=http://localhost:8000 \
  -e LIVE_BASE_URL=http://localhost:8000 -e CORS_ALLOWED_ORIGINS=http://localhost:8000 \
  -e EMAIL_HOST=127.0.0.1 -e EMAIL_PORT=51025 -e EMAIL_USE_TLS=0 \
  -e AWS_S3_ENDPOINT_URL=http://127.0.0.1:59000 -e AWS_ACCESS_KEY_ID=plane-minio \
  -e AWS_SECRET_ACCESS_KEY=plane-minio-secret -e AWS_S3_BUCKET_NAME=uploads -e AWS_REGION=us-east-1 \
  plane-lite
```

`--network host` works on Linux. On macOS or Windows, drop it, add `-p 8000:8000`, and replace
`127.0.0.1` in the URLs with `host.docker.internal`. Keep `AWS_S3_ENDPOINT_URL` on a host the browser
can also reach, because downloads redirect the browser straight to it.

Open <http://localhost:8000> and sign up. Sign-in codes and other emails show up in Mailpit at
<http://localhost:58025>. Leave out the `AWS_*` lines if you didn't start MinIO. Without storage,
anything that uploads a file (project covers, avatars, attachments, editor images) fails with a 503.

### Working on the Go server

```sh
cd apps/server
make test          # unit + contract tests against the Go server (starts db, redis and mail)
```

The contract tests replay recorded Django responses ("goldens") and check that Go answers
identically. Tests that need storage are skipped unless MinIO is up and `PLANE_DEV_S3=1` is set.
[`apps/server/README.md`](apps/server/README.md) covers the layout, the test harness and running
several test stacks side by side. [`apps/server/ROADMAP.md`](apps/server/ROADMAP.md) has the current
status.

The Django code this was ported from lives on the `legacy` branch. Porting another feature means
recording its goldens against that reference; see
[`apps/server/contract/PLAYBOOK.md`](apps/server/contract/PLAYBOOK.md).

### Working on the web app

```sh
corepack enable          # provides the pinned pnpm (on NixOS: corepack enable --install-directory ~/.local/bin)
pnpm install
pnpm turbo run check:types --filter=web
pnpm turbo run build --filter=web
```

For a dev server with hot reload (`pnpm dev --filter=web`, port 3000), set `VITE_API_BASE_URL` to the
Go server's URL in `apps/web/.env` (see `apps/web/.env.example`), and add `http://localhost:3000` to
the server's `CORS_ALLOWED_ORIGINS`.

## Storage: MinIO vs S3 (or R2)

plane-lite stores files (avatars, covers, attachments, editor images) in any S3-compatible bucket.
The server only talks the S3 API, so the provider is just configuration:

| | Use it for | Settings |
|---|---|---|
| **Cloudflare R2** | Production (recommended: no egress fees) | `AWS_S3_ENDPOINT_URL=https://<account-id>.r2.cloudflarestorage.com`, `AWS_REGION=auto`, an R2 API token with Object Read & Write as the key pair |
| **AWS S3** | Production | Endpoint `https://s3.<region>.amazonaws.com`, your region, an IAM key limited to the bucket |
| **MinIO** | Local development and tests only. Off by default | `docker compose -f apps/server/docker-compose.dev.yml --profile s3 up -d minio` (bucket `uploads`, key `plane-minio` / `plane-minio-secret`, port 59000) |

How files move:

- **Uploads go through the server.** Upstream Plane has the browser post files straight to S3
  with a presigned POST. R2 doesn't support presigned POST. So plane-lite gives the browser an
  upload URL on itself (`/api/assets/v2/upload/<id>/`), checks it the way S3 would, and streams
  the file to the bucket. The web app is unchanged, and the bucket needs no CORS rules.
- **Downloads go straight to the bucket.** Asset URLs redirect to a short-lived presigned GET URL
  (`SIGNED_URL_EXPIRATION`, default 1 hour), so file bytes don't pass through the server.
- **Without storage,** the server still starts, but file endpoints answer 503.

To check the Go side against a real bucket, set `CONTRACT_S3_ENDPOINT`, `CONTRACT_S3_ACCESS_KEY`,
`CONTRACT_S3_SECRET_KEY`, `CONTRACT_S3_BUCKET` and `CONTRACT_S3_REGION`, then run the asset tests.

## What's left out

These upstream features are removed from both the server and the web app:

| Feature | Notes |
|---|---|
| Intake (triage inbox) | Projects still get the Triage state, but there's no intake view |
| Publishing / public boards (the `space` app) | No public project pages or deploy boards |
| Analytics | Workspace and project analytics pages. Cycle and module progress and burndown charts stay |
| Public REST API (v1) and API tokens | The web app's own API is all there is |
| Webhooks | |
| OAuth sign-in (Google, GitHub, GitLab, Gitea) | Email + password and email sign-in codes remain |
| Imports, exports and integrations | Jira/GitHub importers, CSV/Excel exports and the integrations settings |
| AI features | Editor AI menu, rephrase, "I'm feeling lucky" |
| Drafts | Closing a new work item with unsaved changes asks before discarding |
| Unsplash cover picker | Upload your own or pick a bundled image |
| God-mode admin app | Instance settings come from environment variables |
| Demo workspace seed data | New workspaces start empty |
| Telemetry | No usage reporting |

Everything else is kept: workspaces, members and invitations; projects; states and labels; work
items with sub-items, relations, links, attachments, comments, reactions and history; cycles;
modules; estimates; saved views; pages with real-time collaboration; stickies and the home
dashboard; notifications (in-app and email); favorites and recents; search; profile pages.

Small behaviour differences from upstream, mostly upstream bugs fixed and response caches
dropped, are listed in [`apps/server/DEVIATIONS.md`](apps/server/DEVIATIONS.md).

## License

plane-lite is a fork of [Plane](https://github.com/makeplane/plane) by Plane Software, Inc. and
contributors, licensed under the [GNU Affero General Public License v3.0](LICENSE.txt). If you run a
modified version as a network service, the AGPL requires you to offer its users the source code.
