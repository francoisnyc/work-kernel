#!/usr/bin/env bash
# Restart only the item service, registering the Types in DIR (test 1 puts
# v2 live this way: the kernel registers Types at start).
set -euo pipefail
cd "$(dirname "$0")/.."
DIR=${1:?types directory}
TOKENS=${2:-kernel/dev-tokens.yaml}   # test 2 passes another file to give the agent a role it should not have
kill "$(cat .run/itemservice.pid)" 2>/dev/null || true
sleep 0.5
DATABASE_URL="postgres://kernel:kernel@127.0.0.1:15432/kernel?sslmode=disable" \
  nohup .bin/itemservice --dev-tokens "$TOKENS" --types "$DIR" --listen 127.0.0.1:18080 > .run/itemservice.log 2>&1 &
echo $! > .run/itemservice.pid
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:18080/healthz >/dev/null && exit 0; sleep 0.25; done
echo "item service did not start"; tail .run/itemservice.log; exit 1
