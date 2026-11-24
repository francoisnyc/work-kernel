#!/usr/bin/env bash
# Creates the systems people may ask access to, once, on an empty kernel.
# Run as ivy (role it_admin): only it_admin writes a system's name and owner.
# Safe to run again: each create carries a fixed request ID, so a second run
# returns the same Items.
set -euo pipefail
PLI="$(cd "$(dirname "$0")/../../camunda-bakeoff/scripts" && pwd)/pli.sh"

"$PLI" --as ivy item create --type system@1 --set f_name=payroll --set f_owner=user:olaf --request-id setup-system-payroll
"$PLI" --as ivy item create --type system@1 --set f_name=ledger  --set f_owner=user:fay  --request-id setup-system-ledger
