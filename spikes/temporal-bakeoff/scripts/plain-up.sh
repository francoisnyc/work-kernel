#!/usr/bin/env bash
# Start the plain-Temporal side: Postgres and a Temporal development server
# in Docker, then the team's one program (API and worker) on the host.
# The shared stubs (../camunda-bakeoff/scripts/stubs-up.sh) must be up.
set -euo pipefail
cd "$(dirname "$0")/.."
SPIKE=$PWD; ROOT=$(cd ../.. && pwd); FIRST=$ROOT/spikes/camunda-bakeoff
mkdir -p .bin .run
curl -sf http://127.0.0.1:18082/healthz >/dev/null || { echo "run scripts/stubs-up.sh first"; exit 1; }
(cd "$ROOT" && go build -o "$SPIKE/.bin/" ./spikes/temporal-bakeoff/cmd/...)
T0=$(date +%s.%N)
docker compose up -d --wait >/dev/null 2>&1
DATABASE_URL="postgres://plain:plain@127.0.0.1:25432/plain?sslmode=disable" TEMPORAL_ADDRESS=127.0.0.1:27233 \
  nohup .bin/ptapp --tokens "$FIRST/kernel/dev-tokens.yaml" --listen 127.0.0.1:28080 --purchase-code "${PURCHASE_CODE:-patched}" > .run/ptapp.log 2>&1 &
echo $! > .run/ptapp.pid
for _ in $(seq 1 80); do curl -sf http://127.0.0.1:28080/healthz >/dev/null && break; sleep 0.25; done
kill -0 "$(cat .run/ptapp.pid)" 2>/dev/null || { echo "ptapp did not start:"; tail -20 .run/ptapp.log; exit 1; }
echo "plain-Temporal side up in $(echo "$(date +%s.%N) - $T0" | bc) s (build not counted)"
