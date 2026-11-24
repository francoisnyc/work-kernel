#!/usr/bin/env bash
cd "$(dirname "$0")/.."
docker compose --profile camunda down -v >/dev/null 2>&1
echo "camunda side down"
