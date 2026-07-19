# 0002 — Redis + asynq over Kafka

**Status:** Accepted

## Context
The platform needs a durable job queue for sends, IMAP polls, and warmup pacing,
surviving process restarts (no silently dropped sends). The prior-art polyglot
reference ran Kafka + Zookeeper + Schema Registry — heavy operational surface even
for local dev, and hard for a small team to run and finish.

## Decision
Use **Redis + asynq** for the job queue and pacing state. All asynq usage is wrapped
in `internal/platform/queue` (single import site).

## Consequences
- Redis-persisted job state satisfies restart-survival without Kafka's operational
  weight; local dev needs only Postgres + Redis.
- At v1 single-node scale this is ample; if throughput ever demands a partitioned
  log, the `queue` wrapper is the one place to swap the backend.
- No Schema Registry / Zookeeper to run or secure.
