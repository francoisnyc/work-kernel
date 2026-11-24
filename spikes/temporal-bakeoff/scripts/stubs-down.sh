#!/usr/bin/env bash
cd "$(dirname "$0")/.."
for n in connstub modelstub; do [ -f .run/$n.pid ] && kill "$(cat .run/$n.pid)" 2>/dev/null; rm -f .run/$n.pid; done
echo "stubs down"
