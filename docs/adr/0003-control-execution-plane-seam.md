# 0003 — Control/execution plane seam (`coreapi`)

**Status:** Accepted

## Context
Workers (send/poll/warmup) will eventually run as a horizontally-scalable fleet,
possibly one per sending IP. A worker that opens Postgres directly couples the
execution plane to the database and blocks that topology. But at v1 scale we don't
want the operational cost of a real internal HTTP API yet.

## Decision
Introduce a `coreapi.Client` interface as the control⇄execution boundary. Worker
packages (`internal/worker/*`) depend ONLY on this interface, never on
`platform/db`. v1 wires an in-process DB-backed implementation
(`coreapi/inprocess`) at the composition root (`cmd/worker`); a future
`coreapi/http` implementation (versioned `/api/v1/internal/*`) swaps in without
changing any worker code.

## Consequences
- The boundary is honored today (worker packages import no `db`), so scaling out
  later is a wiring change, not a rewrite — paid for only when needed.
- Slight indirection now (an interface + one impl) for large future flexibility.
- Decrypted-credential access for the send path will flow through this same seam.
