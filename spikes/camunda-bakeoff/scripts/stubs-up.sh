#!/usr/bin/env bash
# The stubs both sides share, on the host: the kernel repository's connstub
# (the purchase system = its ERP's POST /invoices, the HR directory, the
# email stub) and the bake-off's model stub. They listen on every interface
# so Camunda's containers reach them through host.docker.internal: run this
# only on a machine nobody else can reach (they hold no secrets, only dev data).
set -euo pipefail
cd "$(dirname "$0")/.."
SPIKE=$PWD; ROOT=$(cd ../.. && pwd)
mkdir -p .bin .run
(cd "$ROOT" && go build -o "$SPIKE/.bin/" ./cmd/connstub ./spikes/camunda-bakeoff/cmd/modelstub)
export HR_HOOK_SECRET="whsec_fevc5BcBsz9Qr6vHidUsK2uBHqMkiTWPCXqKlRqwkO4=" # DEV ONLY, as in the kernel's stack
start() { local name=$1; shift; nohup "$@" > ".run/$name.log" 2>&1 & echo $! > ".run/$name.pid"; }
start connstub .bin/connstub --crm-listen 0.0.0.0:18081 --erp-listen 0.0.0.0:18082 \
  --customers kernel/vendors.json --hr-listen 0.0.0.0:18083 --hr-directory kernel/hr-directory.json \
  --hr-hook-secret-env HR_HOOK_SECRET --email-listen 0.0.0.0:18084 --email-read-listen 0.0.0.0:18085
start modelstub .bin/modelstub --listen 0.0.0.0:18090 --log results/modelstub-calls.log
sleep 1
for n in connstub modelstub; do kill -0 "$(cat .run/$n.pid)" 2>/dev/null || { echo "$n did not start"; tail .run/$n.log; exit 1; }; done
echo "stubs up: purchase system :18082, HR :18083, email :18084 (read :18085), model :18090"
