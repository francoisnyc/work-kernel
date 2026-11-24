#!/usr/bin/env bash
# Run the whole bake-off from clean: both sides up, the four tests, results in results/.
# Needs Docker, Go, Python 3, curl and bc. Takes about ten minutes.
set -euo pipefail
cd "$(dirname "$0")/.."
rm -f results/test[0234]*.md results/test1-cfo.md results/*.txt results/*.png results/modelstub-calls.log
scripts/camunda-down.sh; scripts/kernel-down.sh; scripts/stubs-down.sh
(cd ../.. && go build -o spikes/camunda-bakeoff/.bin/ ./cmd/itemservice ./cmd/relay ./cmd/worker ./cmd/migrate ./cmd/pli ./cmd/connstub ./spikes/camunda-bakeoff/cmd/modelstub)
scripts/stubs-up.sh
# Each side starts alone, so its start time and idle memory are its own.
scripts/kernel-up.sh | tee results/test4-start-kernel.txt
sleep 10; scripts/test4-footprint.sh "kernel idle, alone"
scripts/camunda-up.sh | tee results/test4-start-camunda.txt
sleep 20; scripts/test4-footprint.sh "both idle"
cd scripts
python3 test3-inbox.py          # first, on fresh data
(NODE_PATH=$(npm root -g) node tasklist-screenshot.js || echo "screenshot skipped (needs Playwright)")
python3 test2-injection.py
python3 test0-paths.py
python3 test2-compromised.py all
TYPES=kernel/types python3 test1-cfo.py
cd ..
scripts/test4-footprint.sh "both, after the tests"
echo "done: see results/"
