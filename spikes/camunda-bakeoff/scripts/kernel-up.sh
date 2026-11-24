#!/usr/bin/env bash
# Start the kernel side: Postgres and Temporal in Docker, then the kernel's
# own programs, built from this repository's main code (no change to them),
# on the host. Logs and pids go to .run/. Idempotent enough for a spike:
# run scripts/kernel-down.sh first to start clean.
set -euo pipefail
cd "$(dirname "$0")/.."
SPIKE=$PWD
ROOT=$(cd ../.. && pwd)
mkdir -p .bin .run

T0=$(date +%s.%N)
docker compose --profile kernel up -d --wait >/dev/null
(cd "$ROOT" && go build -o "$SPIKE/.bin/" ./cmd/itemservice ./cmd/relay ./cmd/worker ./cmd/migrate ./cmd/pli)

export DATABASE_URL="postgres://kernel:kernel@127.0.0.1:15432/kernel?sslmode=disable"
export TEMPORAL_ADDRESS=127.0.0.1:17233
# DEV ONLY: the HR stub's hook-protocol signing secret (the kernel's own stack uses the same dev value).
export HR_HOOK_SECRET="whsec_fevc5BcBsz9Qr6vHidUsK2uBHqMkiTWPCXqKlRqwkO4="
TYPES=${TYPES:-$SPIKE/kernel/types}

.bin/migrate > .run/migrate.log 2>&1

start() { # name, then the command
  local name=$1; shift
  nohup "$@" > ".run/$name.log" 2>&1 &
  echo $! > ".run/$name.pid"
}
# The shared stubs (scripts/stubs-up.sh) must be up: the HR directory, the
# purchase system (ERP) and the email stub.
curl -sf http://127.0.0.1:18082/healthz >/dev/null || { echo "run scripts/stubs-up.sh first"; exit 1; }
start itemservice .bin/itemservice --dev-tokens kernel/dev-tokens.yaml --types "$TYPES" --listen 127.0.0.1:18080
for _ in $(seq 1 60); do curl -sf http://127.0.0.1:18080/healthz >/dev/null && break; sleep 0.25; done
start relay .bin/relay
CRM_URL=http://127.0.0.1:18081 ERP_URL=http://127.0.0.1:18082 EMAIL_URL=http://127.0.0.1:18084 \
  start worker .bin/worker --connectors kernel/connectors.yaml
sleep 1
for n in itemservice relay worker; do
  kill -0 "$(cat .run/$n.pid)" 2>/dev/null || { echo "$n did not start:"; tail -20 ".run/$n.log"; exit 1; }
done
echo "kernel side up in $(echo "$(date +%s.%N) - $T0" | bc) s (including go build)"
