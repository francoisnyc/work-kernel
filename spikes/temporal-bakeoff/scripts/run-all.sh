#!/usr/bin/env bash
# Run the second bake-off from clean: both sides up, every measure, results in results/.
# Needs Docker, Go, Python 3, curl, psql and bc. About five minutes.
set -euo pipefail
cd "$(dirname "$0")/.."
FIRST=$(cd ../camunda-bakeoff && pwd)
# Kept: timing.txt, notes.md and attacks-plain-access-before-hardening.md (made during the build).
rm -f results/attacks-plain.md results/attacks-kernel.md results/sidedoors.md results/inbox.md results/change-*.md \
      results/footprint.md results/start-*.txt results/modelstub-calls.log
scripts/plain-down.sh; "$FIRST/scripts/kernel-down.sh"; "$FIRST/scripts/stubs-down.sh" >/dev/null; scripts/stubs-down.sh
(cd ../.. && go build -o spikes/temporal-bakeoff/.bin/ ./spikes/temporal-bakeoff/cmd/... && go build -o spikes/camunda-bakeoff/.bin/ ./cmd/itemservice ./cmd/relay ./cmd/worker ./cmd/migrate ./cmd/pli)
scripts/stubs-up.sh
# Each side starts alone first, so its start time and idle memory are its own.
"$FIRST/scripts/kernel-up.sh" 2>/dev/null | tee results/start-kernel.txt
sleep 8; scripts/footprint.sh "kernel idle"
scripts/plain-up.sh | tee results/start-plain.txt
sleep 8; scripts/footprint.sh "both idle"
cd scripts
python3 test_inbox.py            # first, on fresh data
python3 test_attacks.py all      # measure 4
python3 test_sidedoors.py
../scripts/footprint.sh "both, after the tests"
# Measure 2, on fresh data: the plain side as it was before the change, then the change.
./plain-down.sh; PURCHASE_CODE=before ./plain-up.sh >/dev/null
python3 test_change_plain.py
python3 test_change_kernel.py    # restarts the kernel's item service with purchase@2 registered
echo "done: see results/"
