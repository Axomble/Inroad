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

## Status
- Foundation: complete (skeleton, go.mod+go.sum frozen, DB layer, sqlc gen, api spec)
- Task 1 (skeleton/tooling): complete (controller)
- Task 3 (db layer + migrations + sqlc): complete (controller)
- Task 12 (openapi.yaml): complete (controller)
- Wave 1 (parallel): config/log, httpx, crypto, queue, auth, web — dispatched
- Wave 2 (pending): workspace, coreapi+worker, cmd
- Task 14 (deploy+docs): pending
- Final review + finish branch: pending
