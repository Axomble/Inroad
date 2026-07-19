#!/usr/bin/env bash
# Bring up the full local stack for end-to-end testing:
#   Postgres (:5433) + Redis  ->  migrate  ->  seed demo user  ->  API (:8080)  ->  web (:5173)
# Usage: bash scripts/dev-up.sh   (Ctrl-C or `bash scripts/dev-down.sh` to stop)
set -euo pipefail
cd "$(dirname "$0")/.."

export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin:/c/Program Files/Docker/Docker/resources/bin"
export INROAD_ENV=development
export INROAD_HTTP_ADDR=":8080"
export INROAD_JWT_SECRET="${INROAD_JWT_SECRET:-0123456789abcdef0123456789abcdef}"
export INROAD_MASTER_KEY="${INROAD_MASTER_KEY:-MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=}"
export INROAD_DATABASE_URL="${INROAD_DATABASE_URL:-postgres://inroad:inroad@localhost:5433/inroad?sslmode=disable}"
export INROAD_REDIS_ADDR="${INROAD_REDIS_ADDR:-localhost:6379}"

echo "==> Postgres + Redis"
docker compose -f deploy/compose/docker-compose.dev.yml up -d
PG=$(docker compose -f deploy/compose/docker-compose.dev.yml ps -q postgres)
until docker exec "$PG" pg_isready -U inroad >/dev/null 2>&1; do sleep 1; done

echo "==> migrate + seed"
go run ./cmd/migrate up
go run ./cmd/seed || true   # demo@inroad.test / demodemo (idempotent-ish: ignores duplicate)

echo "==> API on :8080"
go run ./cmd/inroad & echo $! > /tmp/inroad-api.pid

echo "==> web on :5173"
( cd web && npm run dev -- --host ) & echo $! > /tmp/inroad-web.pid

echo
echo "Stack up:  web http://localhost:5173   api http://localhost:8080"
echo "Login as:  demo@inroad.test / demodemo"
echo "Stop with: bash scripts/dev-down.sh"
wait
