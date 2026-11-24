#!/usr/bin/env bash
# The kernel's CLI against the bake-off's item service: scripts/pli.sh --as ana item get ...
cd "$(dirname "$0")/.."
exec .bin/pli --server http://127.0.0.1:18080 --tokens kernel/dev-tokens.yaml "$@"
