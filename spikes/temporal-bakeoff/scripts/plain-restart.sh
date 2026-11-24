#!/usr/bin/env bash
# Restart only the team's program (a deploy), keeping Temporal and Postgres
# and every request in flight: scripts/plain-restart.sh before|naive|patched
set -euo pipefail
cd "$(dirname "$0")/.."
FIRST=$(cd ../camunda-bakeoff && pwd)
kill "$(cat .run/ptapp.pid)" 2>/dev/null || true
sleep 0.7
DATABASE_URL="postgres://plain:plain@127.0.0.1:25432/plain?sslmode=disable" TEMPORAL_ADDRESS=127.0.0.1:27233 \
  nohup .bin/ptapp --tokens "$FIRST/kernel/dev-tokens.yaml" --listen 127.0.0.1:28080 --purchase-code "${1:-patched}" > .run/ptapp.log 2>&1 &
echo $! > .run/ptapp.pid
for _ in $(seq 1 80); do curl -sf http://127.0.0.1:28080/healthz >/dev/null && exit 0; sleep 0.25; done
echo "ptapp did not start"; tail .run/ptapp.log; exit 1
