-- name: RecordWorkerProviderSignals :exec
-- Persist ONE window of per-verdict counts for a worker, in a single round trip.
--
-- The varying columns arrive as parallel arrays and are unnested into rows; the
-- three that repeat for a whole batch (worker, window bounds) are passed once as
-- scalars. One statement, so the window lands atomically: a reader never sees
-- half a window and mistake it for a quiet one.
--
-- This deliberately does NOT use sqlc's :copyfrom. COPY is the right tool for
-- thousands of rows, and a flush carries at most one row per (provider,
-- operation, reason) actually observed — dozens at the very worst. What it would
-- cost is permanent: :copyfrom adds CopyFrom to the generated DBTX interface,
-- which every hand-written implementation in the repo then owes, and it already
-- broke an unrelated test double (countingDBTX in the inprocess package) that
-- has nothing to do with fleet signals. A shared interface is the wrong place to
-- pay for an optimisation this size.
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
SELECT
    @worker_id::text,
    unnest(@providers::text[]),
    unnest(@operations::text[]),
    unnest(@reasons::text[]),
    unnest(@events::bigint[]),
    @window_start::timestamptz,
    @window_end::timestamptz;

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

-- name: ListWorkspaceFleetWorkers :many
-- The operator's fleet list: every worker THIS WORKSPACE has a mailbox pinned
-- to, with the registry facts that make a bad one visible.
--
-- WORKSPACE-PINNED THROUGH THE ASSIGNMENT, and that is the whole shape of the
-- query rather than a filter bolted on. `workers` is global infrastructure
-- (migration 000017's trust-domain split) and enumerating it would tell one
-- tenant how large another's deployment is; joining THROUGH
-- mailbox_worker_assignments answers the narrower, honest question -- "which
-- egress IPs does MY mail leave from" -- and a workspace with no assignments
-- gets an empty list rather than a fleet census.
--
-- It is an inner JOIN for that reason: a worker carrying none of this
-- workspace's mailboxes cannot affect this workspace's sending, and returning it
-- anyway would be the census this shape exists to avoid.
--
-- The counts are this workspace's own footprint on each worker, never the
-- fleet-wide occupancy ListPlacementCandidates scores on: an operator needs to
-- know how much of THEIR mail sits behind one IP, and how much of that is
-- degraded traffic, and neither question has a cross-tenant answer they are
-- entitled to.
--
-- Liveness is deliberately NOT computed here. last_seen_at is returned raw and
-- the caller applies the heartbeat window, so the "is it live" rule lives in one
-- Go constant next to the reasoning for its value rather than as a parameter
-- every call site could pass differently.
SELECT
    w.worker_id,
    w.egress_ip,
    w.id_family,
    w.last_seen_at,
    count(a.mailbox_id)::bigint AS workspace_mailboxes,
    count(a.mailbox_id) FILTER (WHERE a.band = 'degraded')::bigint AS degraded_mailboxes,
    min(a.assigned_at)::timestamptz AS first_assigned_at
FROM workers w
JOIN mailbox_worker_assignments a
  ON a.worker_id = w.worker_id
 AND a.workspace_id = @workspace_id::uuid
GROUP BY w.worker_id, w.egress_ip, w.id_family, w.last_seen_at
ORDER BY w.worker_id ASC;

-- name: RollupWorkspaceFleetProviderSignals :many
-- What the PROVIDERS have been telling each of this workspace's workers lately,
-- rolled up per (worker, provider, operation).
--
-- SCOPED TO THE SAME WORKER SET as ListWorkspaceFleetWorkers, by the same
-- subquery rather than by an id array passed in from Go: a caller that forgot to
-- narrow such an array would read the whole fleet's counters, and a predicate
-- that is part of the statement cannot be forgotten.
--
-- WHAT THESE COUNTERS ARE, AND ARE NOT. worker_provider_signals carries no
-- workspace_id and honestly cannot (migration 20260914150140): several
-- workspaces' mailboxes share one worker, and a provider's verdict is about the
-- EGRESS IP, not about whose mail happened to trigger it. So a row here is a
-- shared-fate aggregate -- it includes events other tenants' mailboxes on the
-- same worker caused -- and that is the fact the operator needs rather than a
-- leak around one: the risk being measured is shared by construction, which is
-- why the table is per-worker at all. Nothing per-tenant is recoverable from a
-- sum, and no mailbox, address or tenant row is returned.
--
-- THE GROUPINGS ARE THE POINT. attempts and successes are returned together
-- because a success count alone cannot tell a healthy worker from an idle one,
-- and an attempt count alone cannot tell a busy worker from a failing one.
-- auth_failures is its OWN column rather than folded into a "soft failures"
-- bucket: it is the provider-side signal that predicts an IP being challenged,
-- and it is why this table was collected at all.
--
-- rejected is returned but kept out of every per-IP bucket, because
-- providersignal's own doc is explicit that a permanent non-security 5xx is
-- about the RECIPIENT (dead address, oversized message) and must never be read
-- as evidence against the worker. It is here so attempts reconcile, not to be
-- scored.
--
-- attempts is the total across every reason INCLUDING 'other', so the five
-- classified columns can never silently fail to account for the whole: whatever
-- attempts exceeds their sum by is exactly the unclassified remainder.
SELECT
    s.worker_id,
    s.provider,
    s.operation,
    sum(s.events)::bigint AS attempts,
    coalesce(sum(s.events) FILTER (WHERE s.reason = 'ok'), 0)::bigint AS successes,
    coalesce(sum(s.events) FILTER (WHERE s.reason = 'auth_failed'), 0)::bigint AS auth_failures,
    coalesce(sum(s.events) FILTER (WHERE s.reason IN ('rate_limited', 'throttled')), 0)::bigint AS throttled,
    coalesce(sum(s.events) FILTER (WHERE s.reason IN ('blocked', 'unreachable')), 0)::bigint AS blocked,
    coalesce(sum(s.events) FILTER (WHERE s.reason = 'rejected'), 0)::bigint AS rejected
FROM worker_provider_signals s
WHERE s.window_end >= @signals_since::timestamptz
  AND s.worker_id IN (
      SELECT a.worker_id
      FROM mailbox_worker_assignments a
      WHERE a.workspace_id = @workspace_id::uuid
  )
GROUP BY s.worker_id, s.provider, s.operation
ORDER BY s.worker_id ASC, s.provider ASC, s.operation ASC;
