#!/bin/sh
# Runs the plane-lite API (Go) and the collaboration server (apps/live) side
# by side. The Go server is the front door on $PORT and proxies /live/ to
# live on 127.0.0.1:$LIVE_PORT. If either process exits, the other is stopped
# and the container exits, so the orchestrator's restart policy takes over.
#
#   plane-lite              run both (default)
#   plane-lite <args...>    run `plane <args...>` instead (migrate, run-job, ...)
set -eu

if [ "$#" -gt 0 ]; then
	exec plane "$@"
fi

: "${LIVE_PORT:=3100}"
export LIVE_UPSTREAM="${LIVE_UPSTREAM:-http://127.0.0.1:$LIVE_PORT}"

# Apply migrations before live starts calling the API.
plane migrate

# live reads PORT for its own port and calls the API on the loopback. It
# shares REDIS_URL and REDIS_KEY_PREFIX with the API (same default prefix).
# Its secret only guards admin endpoints nothing here calls; generate one if
# unset.
LIVE_SERVER_SECRET_KEY="${LIVE_SERVER_SECRET_KEY:-$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')}" \
	API_BASE_URL="${LIVE_API_BASE_URL:-http://127.0.0.1:$PORT}" \
	REDIS_KEY_PREFIX="${REDIS_KEY_PREFIX:-plane:}" \
	PORT="$LIVE_PORT" \
	node /app/apps/live &
live=$!

plane serve &
api=$!

stop() {
	kill -TERM "$live" "$api" 2>/dev/null || true
}
trap 'stop; wait; exit 0' TERM INT

while kill -0 "$live" 2>/dev/null && kill -0 "$api" 2>/dev/null; do
	sleep 2
done
echo "plane-lite: a process exited; stopping" >&2
stop
wait
exit 1
