# 0001 — pgx + sqlc over an ORM

**Status:** Accepted

## Context
Inroad is a deliverability tool: the hot paths (send/poll loops, bounce-rate and
reputation windows, funnel analytics, ramp math) are query-heavy and need SQL we
can read, `EXPLAIN`, and tune. An ORM (GORM/Ent) speeds trivial CRUD but hides
the SQL and fights hand-optimization exactly where it matters; you end up dropping
to raw SQL for the important 20% anyway.

## Decision
Use **pgx/v5 + sqlc**: write SQL in `internal/platform/db/queries/*.sql`, generate
type-safe Go into `gen/`. Add **squirrel** as an escape hatch for genuinely dynamic
queries (e.g. dynamic segments) that sqlc can't express.

## Consequences
- SQL stays visible and reviewable; no ORM magic; compile-time type safety.
- Small codegen step (`sqlc generate`) in the workflow.
- Domains own their data access via a `Store` interface (see ADR 0003 / CLAUDE.md),
  keeping sqlc types behind a boundary.
- UUID columns require the google/uuid override + pgx codec registration (done in
  `sqlc.yaml` + `db.Connect`).
