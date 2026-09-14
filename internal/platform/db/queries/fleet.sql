-- name: RecordWorkerProviderSignals :copyfrom
-- Persist ONE window of per-worker provider verdicts.
--
-- :copyfrom (pgx COPY) rather than N round trips: a window is written from the
-- worker's flush loop on a timer, and the whole point of accumulating in memory
-- is that reporting costs nothing on the send path. COPY still enforces every
-- CHECK and NOT NULL on the table, so a stray reason or a non-positive delta
-- fails the flush rather than being written.
--
-- worker_id/window_start/window_end repeat on every row of a batch. That is not
-- redundancy to normalise away: each row must stand alone as "this worker saw
-- this many of this verdict between these two instants", because aggregation is
-- a SUM over a time range and a reader must never have to join to learn when a
-- delta applies.
--
-- Every row is a DELTA for [window_start, window_end) -- never a running total.
-- There is deliberately no conflict handling: two flushes from the same worker
-- in the same window are two windows' worth of events and must both be summed,
-- not collapsed. The BIGSERIAL id keeps that true even when two flushes land on
-- the same instant.
--
-- `worker_provider_signals` is global infrastructure state like `workers`, not
-- tenant data, so there is no workspace pin (see the table's migration).
INSERT INTO worker_provider_signals (worker_id, provider, operation, reason, events, window_start, window_end)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: PurgeWorkerProviderSignals :one
-- Retention sweep: drop signal windows past the 30-day window any per-worker
-- rollup reads. Deletes by age alone and returns only a count, so it can neither
-- surface nor cross tenant data (there is none here to cross).
--
-- 30 days rather than the 90 used for warmup evidence: a window delta answers
-- "how is this egress IP being treated RIGHT NOW", and the oldest question any
-- placement scorer or dashboard asks of it is a month. Batched at 5000 rows like
-- every other purge, to cap one sweep's lock and IO footprint.
WITH deleted_signals AS (
    DELETE FROM worker_provider_signals
    WHERE id IN (
        SELECT id FROM worker_provider_signals
        WHERE window_end < now() - interval '30 days'
        ORDER BY window_end LIMIT 5000
    )
    RETURNING 1
)
SELECT count(*)::bigint AS deleted_rows FROM deleted_signals;

-- name: InsertFleetDecision :exec
-- Append one automated fleet decision.
--
-- mailbox_id/workspace_id travel together or not at all (the table's pairing
-- CHECK), and the composite FK makes a cross-tenant pair unrepresentable rather
-- than merely rejected, so a mismatched pair fails the statement instead of
-- writing a row naming another tenant's mailbox. That is why this is a plain
-- INSERT and not the INSERT ... SELECT ... FROM mailboxes shape the tenant
-- tables use: the constraint already enforces what that SELECT would check, and
-- a silent zero-row insert would lose a decision rather than report it.
INSERT INTO fleet_decisions (kind, worker_id, mailbox_id, workspace_id, reason, triggered_by)
VALUES (
    @kind::text,
    sqlc.narg('worker_id')::text,
    sqlc.narg('mailbox_id')::uuid,
    sqlc.narg('workspace_id')::uuid,
    @reason::text,
    @triggered_by::text
);

-- name: ListFleetDecisionsForMailbox :many
-- "Why is this mailbox on this worker?" -- the question the table exists to
-- answer, newest first. workspace-pinned, because a decision naming a mailbox is
-- tenant data (see the table's migration for why it carries a workspace at all).
SELECT id, kind, worker_id, mailbox_id, workspace_id, reason, triggered_by, created_at
FROM fleet_decisions
WHERE mailbox_id = @mailbox_id::uuid
  AND workspace_id = @workspace_id::uuid
ORDER BY created_at DESC, id DESC
LIMIT @row_limit::int;

-- name: PurgeFleetDecisions :one
-- Retention sweep over the decision log, by age alone, returning a count.
--
-- 90 days rather than the signals' 30: a decision is the record of something
-- that HAPPENED to a mailbox, and "when did this mailbox move, and why" is a
-- question asked long after the counters that informed it have stopped
-- mattering. Same batching and same reasoning as every other purge (invariant
-- 55): nothing in the application ever deletes from this table.
WITH deleted_decisions AS (
    DELETE FROM fleet_decisions
    WHERE id IN (
        SELECT id FROM fleet_decisions
        WHERE created_at < now() - interval '90 days'
        ORDER BY created_at LIMIT 5000
    )
    RETURNING 1
)
SELECT count(*)::bigint AS deleted_rows FROM deleted_decisions;
