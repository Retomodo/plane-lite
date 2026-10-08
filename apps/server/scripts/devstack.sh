#!/usr/bin/env bash
# Runs an isolated dev/test stack per slot, so several contract-test runs
# (e.g. parallel porting agents) never share a database, Redis or mailbox.
#
#   scripts/devstack.sh N up          # db, redis, mail and the Django reference
#   scripts/devstack.sh N down        # stop and discard everything
#   scripts/devstack.sh N env         # print the exports for `go test`
#   scripts/devstack.sh N psql [DB]   # psql into plane_test (or DB, e.g. plane_ref)
#   scripts/devstack.sh N shell       # Django shell on the reference (reads stdin)
#   scripts/devstack.sh N compose ... # any other docker compose command
#
# PLANE_DEV_S3=1 also starts MinIO (compose profile s3), an optional local S3
# stand-in for the file asset scenarios; export it for `go test` too so they
# run instead of skipping (see contract/env.go).
#
# Slot 0 is the default stack (the ports in docker-compose.dev.yml); slot N
# adds N*10 to each port. The Go harness picks its slot from CONTRACT_SLOT.
set -euo pipefail
slot=${1:?slot number}
cmd=${2:?command}
shift 2
cd "$(dirname "$0")/.."

off=$((slot * 10))
if [ "$slot" = 0 ]; then
	export PLANE_DEV_PROJECT=plane-lite-dev
else
	export PLANE_DEV_PROJECT=plane-lite-dev-s$slot
fi
export PLANE_DEV_DB_PORT=$((55432 + off))
export PLANE_DEV_REDIS_PORT=$((56379 + off))
export PLANE_DEV_SMTP_PORT=$((51025 + off))
export PLANE_DEV_MAILPIT_PORT=$((58025 + off))
export PLANE_DEV_REFERENCE_PORT=$((58000 + off))
export PLANE_DEV_MINIO_PORT=$((59000 + off))
compose=(docker compose -f docker-compose.dev.yml --profile reference)
if [ "${PLANE_DEV_S3:-0}" = 1 ]; then
	compose+=(--profile s3)
fi

case "$cmd" in
up)
	"${compose[@]}" up -d --wait "$@"
	;;
down)
	# Every profile, so a MinIO started with PLANE_DEV_S3=1 goes too.
	"${compose[@]}" --profile s3 down -v "$@"
	;;
env)
	echo "export CONTRACT_SLOT=$slot"
	if [ "${PLANE_DEV_S3:-0}" = 1 ]; then
		echo "export PLANE_DEV_S3=1"
	fi
	;;
psql)
	"${compose[@]}" exec -T db psql -U plane -d "${1:-plane_test}" "${@:2}"
	;;
shell)
	"${compose[@]}" exec -T reference python manage.py shell
	;;
compose)
	"${compose[@]}" "$@"
	;;
*)
	echo "unknown command: $cmd" >&2
	exit 2
	;;
esac
