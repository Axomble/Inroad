# Inroad Scaffold — Progress Ledger

Branch: `scaffold`
Plan: `docs/superpowers/plans/2026-07-19-inroad-repository-scaffold.md`

## Approach
Parallel-agent execution (user-directed). Controller lays shared foundation
(go.mod frozen via tools_anchor.go, DB layer, sqlc gen, api/openapi.yaml);
agents implement disjoint dirs, do NOT touch go.mod or git; controller commits.

## Toolchain
Go/sqlc/migrate installed but NOT on default PATH. Every Bash go command must
prefix: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`.

## Naming convention (user-set)
Frontend files = kebab-case (login-form.tsx, empty-api.ts). Go backend = lowercase
Go convention (underscore only for _test.go / build tags). Recorded in CLAUDE.md.

## Status
- Foundation: complete (commit b6fa13d) — skeleton, go.mod+go.sum frozen, DB layer, sqlc gen, api spec
- Task 1 (skeleton/tooling): complete (controller, b6fa13d)
- Task 3 (db layer + migrations + sqlc): complete (controller, b6fa13d)
- Task 12 (openapi.yaml): complete (controller, b6fa13d)
- CLAUDE.md: complete (commit 9184844)
- Wave 1 Go (config/log, httpx, crypto, queue, auth): complete (commit 115189c, all tests pass)
- Task 13 web: built & passing (codegen/vitest/build all PASS); kebab-case rename in progress
- Wave 2 (in flight): workspace (Task 8+9), coreapi+worker (Task 10)
- Task 11 (cmd entrypoints): pending — needs workspace+coreapi+worker
- Task 14 (deploy+docs): pending
- Integration gate (docker db-up, run -tags=integration): pending
- Final review + finish branch: pending
