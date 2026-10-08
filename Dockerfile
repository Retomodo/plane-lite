# syntax=docker/dockerfile:1.7
#
# plane-lite: the whole app in one image.
#
#   docker build -t plane-lite .
#   docker run --env-file plane-lite.env -p 8000:8000 plane-lite
#
# One port (8000) serves everything. The Go server (apps/server) answers /api/
# and /auth/, serves the web app's static build and proxies /live/
# (websockets included) to the collaboration server (apps/live), which runs
# beside it on 127.0.0.1. Postgres, Redis and S3/R2 are external, set through
# env. See apps/server/deploy/README.md.

ARG NODE_IMAGE=node:22-alpine
ARG GO_IMAGE=golang:1.26-alpine
ARG TURBO_VERSION=2.10.11

# -----------------------------------------------------------------------------
# Node workspace: web and live are pruned and installed separately, so the
# runtime only carries live's dependencies
# -----------------------------------------------------------------------------
FROM ${NODE_IMAGE} AS node-base
ENV PNPM_HOME="/pnpm" PATH="/pnpm:/pnpm/bin:$PATH" TURBO_TELEMETRY_DISABLED=1
RUN apk add --no-cache libc6-compat && corepack enable

FROM node-base AS pruner
ARG TURBO_VERSION
WORKDIR /app
RUN pnpm add -g turbo@${TURBO_VERSION}
COPY . .
RUN turbo prune web --docker --out-dir=out/web && turbo prune live --docker --out-dir=out/live

FROM node-base AS web-build
WORKDIR /app
COPY .gitignore .npmrc ./
COPY --from=pruner /app/out/web/json/ .
COPY --from=pruner /app/out/web/pnpm-lock.yaml ./pnpm-lock.yaml
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store pnpm fetch --store-dir=/pnpm/store
COPY --from=pruner /app/out/web/full/ .
COPY turbo.json turbo.json
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store CI=true pnpm install --offline --frozen-lockfile --store-dir=/pnpm/store
# The web app talks to its own origin: API at /api and /auth, live at /live.
ENV VITE_API_BASE_URL="" VITE_WEB_BASE_URL="" VITE_LIVE_BASE_URL="" VITE_LIVE_BASE_PATH="/live" \
    VITE_ADMIN_BASE_URL="" VITE_ADMIN_BASE_PATH="/god-mode" VITE_SPACE_BASE_URL="" VITE_SPACE_BASE_PATH="/spaces"
RUN pnpm turbo run build --filter=web

FROM node-base AS live-build
WORKDIR /app
COPY .gitignore .npmrc ./
COPY --from=pruner /app/out/live/json/ .
COPY --from=pruner /app/out/live/pnpm-lock.yaml ./pnpm-lock.yaml
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store pnpm fetch --store-dir=/pnpm/store
COPY --from=pruner /app/out/live/full/ .
COPY turbo.json turbo.json
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store CI=true pnpm install --offline --frozen-lockfile --store-dir=/pnpm/store
RUN pnpm turbo run build --filter=live
# A self-contained copy of live with production dependencies only.
RUN --mount=type=cache,id=pnpm-store,target=/pnpm/store \
    pnpm --filter=live deploy --prod --legacy --store-dir=/pnpm/store /prod/live

# -----------------------------------------------------------------------------
# Go API server
# -----------------------------------------------------------------------------
FROM ${GO_IMAGE} AS go-build
WORKDIR /src
COPY apps/server/go.mod apps/server/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY apps/server/ .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/plane ./cmd/plane

# -----------------------------------------------------------------------------
# Runtime
# -----------------------------------------------------------------------------
FROM ${NODE_IMAGE} AS runtime
RUN apk add --no-cache tini ca-certificates \
    && rm -rf /usr/local/lib/node_modules/npm /usr/local/bin/npm /usr/local/bin/npx
WORKDIR /app

COPY --from=live-build /prod/live ./apps/live

COPY --from=web-build /app/apps/web/build/client ./web
COPY --from=go-build /out/plane /usr/local/bin/plane
COPY apps/server/deploy/entrypoint.sh /usr/local/bin/plane-lite

ENV PORT=8000 \
    STATIC_DIR=/app/web \
    LIVE_PORT=3100 \
    LIVE_BASE_PATH=/live \
    NODE_ENV=production
EXPOSE 8000
USER node
ENTRYPOINT ["/sbin/tini", "-g", "--", "/usr/local/bin/plane-lite"]
