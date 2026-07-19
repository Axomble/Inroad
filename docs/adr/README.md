# Architecture Decision Records

Short records of significant, point-in-time decisions and their rationale, so
they aren't silently reversed later. One file per decision, numbered.

Format (keep it short): **Context** (the problem/forces) → **Decision** (what we
chose) → **Consequences** (trade-offs, what it enables/costs). Status is one of
Accepted / Superseded / Deprecated.

| # | Decision | Status |
|---|---|---|
| [0001](0001-sqlc-over-orm.md) | pgx + sqlc over an ORM | Accepted |
| [0002](0002-redis-asynq-over-kafka.md) | Redis + asynq over Kafka | Accepted |
| [0003](0003-control-execution-plane-seam.md) | control/execution plane seam (`coreapi`) | Accepted |
| [0004](0004-ssrf-guard-default-allow-private.md) | SSRF guard, private allowed by default | Accepted |
| [0005](0005-apache-2.0-license.md) | Apache 2.0 license | Accepted |
