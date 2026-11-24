#!/usr/bin/env bash
# Stop the kernel side and delete its data (Temporal's dev server keeps it in memory anyway).
cd "$(dirname "$0")/.."
for f in .run/itemservice.pid .run/relay.pid .run/worker.pid; do [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null; rm -f "$f"; done
docker compose --profile kernel down -v >/dev/null 2>&1
echo "kernel side down"
