# Deploying plane-lite

The whole app is one image, built from the repo root [`Dockerfile`](../../../Dockerfile):

| Path | Served by |
|---|---|
| `/api/`, `/auth/` | the Go API server (`apps/server`) |
| `/live/` (websockets) | the collaboration server (`apps/live`), proxied by Go to `127.0.0.1:$LIVE_PORT` |
| everything else | the web app's static build (`apps/web`), with `index.html` for client-side routes |

The container listens on port 8000. Postgres, Redis and object storage are external, set
through env. Background jobs and the periodic jobs (notification digests, cleanups) run inside
the Go process on a Postgres-backed queue, so nothing else needs to run beside it. The
container must stay running: the periodic jobs only fire while the process is up.

## Build and run

```sh
docker build -t plane-lite .
cp apps/server/deploy/plane-lite.env.example plane-lite.env   # fill it in
docker run -d --name plane-lite --restart unless-stopped \
  --env-file plane-lite.env -p 127.0.0.1:8000:8000 plane-lite
```

On start the container runs the migrations (`plane migrate`), then starts live and the API. If
either process exits, the container exits, so use a restart policy. One-off commands go through
the same entrypoint, e.g. `docker run --rm --env-file plane-lite.env plane-lite migrate`.

An empty database gets Plane's schema on first start. An existing Plane database (Django migration
0122) can be used as is; everyone signs in again.

## Environment

See [`plane-lite.env.example`](plane-lite.env.example). Required: `SECRET_KEY`, `DATABASE_URL`,
`REDIS_URL`, and in practice the R2 settings (`AWS_*`), since project covers, avatars,
attachments and editor images need them. The public URL goes in `WEB_URL`, `APP_BASE_URL`, `LIVE_BASE_URL` and
`CORS_ALLOWED_ORIGINS`. Set at build time and not normally changed: `STATIC_DIR=/app/web`,
`LIVE_PORT=3100` and `LIVE_BASE_PATH=/live`.

## nginx

TLS ends at nginx, which forwards everything to the container. Compression happens here too:
the Go server doesn't gzip.

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    http2 on;
    server_name plan.rotomodo.com;
    # ssl_certificate ...; ssl_certificate_key ...;

    # Uploads: keep at or above FILE_SIZE_LIMIT.
    client_max_body_size 10m;

    gzip on;
    gzip_proxied any;
    gzip_min_length 1024;
    gzip_types text/plain text/css application/json application/javascript text/javascript image/svg+xml;

    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        # Overwrite, don't append: the API rate-limits sign-in by the first entry.
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        # Collaboration websockets stay open for a long time.
        proxy_read_timeout 1h;
    }
}
```
