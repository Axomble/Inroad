#!/usr/bin/env bash
# Tear down the local e2e stack started by dev-up.sh.
set -uo pipefail
cd "$(dirname "$0")/.."
export PATH="$PATH:/c/Program Files/Docker/Docker/resources/bin"

for pidfile in /tmp/inroad-api.pid /tmp/inroad-web.pid; do
  [ -f "$pidfile" ] && kill "$(cat "$pidfile")" 2>/dev/null; rm -f "$pidfile"
done
docker compose -f deploy/compose/docker-compose.dev.yml down
echo "stack stopped."
