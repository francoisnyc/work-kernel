#!/usr/bin/env bash
# Start the Camunda side and set it up. The connector runtimes log in as
# users the setup creates, so they start after it.
set -euo pipefail
cd "$(dirname "$0")/.."
T0=$(date +%s.%N)
docker compose --profile camunda up -d --wait orchestration >/dev/null 2>&1
T1=$(date +%s.%N)
scripts/camunda-setup.sh
docker compose --profile camunda up -d --wait connectors agent-connectors >/dev/null 2>&1
T2=$(date +%s.%N)
echo "camunda side up in $(echo "$T2 - $T0" | bc) s: orchestration cluster healthy after $(echo "$T1 - $T0" | bc) s, then users set up and both connector runtimes healthy"
scripts/camunda-deploy.sh
