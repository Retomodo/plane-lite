#!/usr/bin/env bash
# Prints the SQL Django runs for a list of requests (see
# contract/reference/capture_sql.py for the file format).
#
#   scripts/capture-sql.sh SLOT REQUESTS_FILE
set -euo pipefail
slot=${1:?slot}
file=${2:?requests file}
dir="$(dirname "$0")"
"$dir/devstack.sh" "$slot" compose cp "$file" reference:/tmp/capture_requests.txt >/dev/null 2>&1
"$dir/devstack.sh" "$slot" shell < "$dir/../contract/reference/capture_sql.py" 2>/dev/null | grep -v '^{"levelname"'
