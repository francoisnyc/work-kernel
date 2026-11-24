#!/usr/bin/env bash
# Stop the plain-Temporal side and delete its data.
cd "$(dirname "$0")/.."
[ -f .run/ptapp.pid ] && kill "$(cat .run/ptapp.pid)" 2>/dev/null; rm -f .run/ptapp.pid
docker compose down -v >/dev/null 2>&1
echo "plain-Temporal side down"
