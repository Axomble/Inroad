-- name: UpsertWorker :exec
-- Heartbeat: register or refresh this worker's row. egress_ip is recorded for
-- observability; id_family records which identity source produced worker_id
-- (ipv4 | ipv6 | hostname | override — see internal/platform/workerid), so a
-- NAT'd/hostname-derived worker is diagnosable from an IP-derived one without
-- cross-referencing logs; last_seen_at drives the live-worker window in the
-- assigner.
INSERT INTO workers (worker_id, egress_ip, id_family, last_seen_at)
VALUES ($1, $2, $3, now())
ON CONFLICT (worker_id)
DO UPDATE SET egress_ip = EXCLUDED.egress_ip, id_family = EXCLUDED.id_family, last_seen_at = now();

-- name: GetLiveMailboxWorkerAssignment :one
-- Existing assignment for a mailbox, but ONLY if the assigned worker is still
-- live (heartbeat at or after live_since). Workspace-pinned: tenant data, so a
-- foreign workspace_id matches zero rows.
--
-- The liveness join is the whole point. An assignment whose worker has stopped
-- heartbeating routes to "w:<dead-id>" — a queue no process consumes — so its
-- tasks neither run nor fail nor alert; the mailbox silently stops sending.
-- Treating a dead worker's assignment as absent lets the caller reassign it.
-- There is deliberately no FK from mailbox_worker_assignments to workers: the
-- assignment outlives a worker restart that reuses the same id (the common
-- case, and the one where keeping the pin preserves egress-IP stability).
--
-- band travels with the row (fleet F5: risk-band segregation) so the caller can
-- compare it against the mailbox's CURRENT computed band without a second
-- query: a mismatch (the mailbox's warmup lane moved since this row was
-- written) is treated exactly like a dead worker — fall through and reassign.
SELECT a.worker_id, a.band
FROM mailbox_worker_assignments a
JOIN workers w ON w.worker_id = a.worker_id
WHERE a.mailbox_id = $1
  AND a.workspace_id = $2
  AND w.last_seen_at >= @live_since::timestamptz;

-- name: PickLeastLoadedWorker :one
-- The least-loaded LIVE worker (heartbeat at or after live_since), with NO band
-- filter. Used only when the fleet has at most one live worker (self-host: there
-- is no second worker to segregate onto, so segregation must not apply — refusing
-- would stop self-host from sending) — the band-aware picks below are used
-- otherwise. Load is the current assignment count across ALL workspaces — workers
-- are global infra, so balancing is fleet-wide, not per-tenant. Deterministic
-- worker_id tie-break. No live worker => zero rows (the caller falls back to the
-- shared default queue).
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
ORDER BY (
    SELECT count(*) FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
) ASC, w.worker_id ASC
LIMIT 1;

-- name: CountLiveWorkers :one
-- How many workers are currently live. Drives the self-host bypass: at most one
-- live worker means there is no placement CHOICE to make, so AssignMailboxWorker
-- skips band matching entirely and falls back to PickLeastLoadedWorker — the
-- exact pre-F5 behaviour, byte for byte, for the single-worker topology.
SELECT count(*) FROM workers WHERE last_seen_at >= @live_since::timestamptz;

-- name: PickLeastLoadedWorkerForBand :one
-- The least-loaded LIVE worker that ALREADY carries at least one live assignment
-- in this band. Requirement 2 (strict segregation): if this returns no row, the
-- caller tries PickIdleLiveWorker next and refuses only if THAT also finds
-- nothing — never falls back to an off-band worker.
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
  AND EXISTS (
      SELECT 1 FROM mailbox_worker_assignments a
      WHERE a.worker_id = w.worker_id AND a.band = @band::text
  )
ORDER BY (
    SELECT count(*) FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
) ASC, w.worker_id ASC
LIMIT 1;

-- name: PickIdleLiveWorker :one
-- A live worker carrying NO assignments at all (any band). This is the
-- promotion path (requirement 3): only a genuinely idle worker may be adopted
-- into a band — never one already carrying another band's mailboxes, which
-- PickLeastLoadedWorkerForBand's EXISTS clause on the SAME band already
-- excludes it from matching, but this query additionally excludes an off-band
-- worker with EXISTING load from being mistaken for idle. Deterministic
-- worker_id tie-break, matching the other picks.
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
  AND NOT EXISTS (
      SELECT 1 FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
  )
ORDER BY w.worker_id ASC
LIMIT 1;

-- name: InsertMailboxWorkerAssignment :one
-- Persist an assignment (with its risk band, fleet F5). Self-enforcing tenancy
-- (defense in depth): the row is written ONLY when the mailbox truly belongs to
-- the workspace, so a mismatched (mailbox, workspace) pair inserts zero rows and
-- RETURNING yields pgx.ErrNoRows — the caller maps that to a cross-tenant
-- rejection.
--
-- On a mailbox_id conflict the row is claimed for the incoming (worker, band)
-- ONLY if the incumbent is BOTH live AND already in the same band as the
-- incoming write; otherwise the incumbent is replaced. That single rule serves
-- three callers:
--
--   * concurrent first-send race — both racers computed the SAME target band for
--     this mailbox (it hasn't changed mid-race), so whichever inserted first
--     wins and the loser's EXCLUDED.band matches the winner's stored band; the
--     existing row wins unchanged and both resolve to the same queue.
--   * reassignment after a worker died — the incumbent is not live, so the row
--     moves to the caller's freshly-picked worker instead of being pinned to a
--     queue nobody consumes.
--   * a mailbox's warmup lane changed band since it was last assigned — the
--     incumbent is live but its stored band now disagrees with EXCLUDED.band
--     (the caller already re-picked a worker matching the NEW band before
--     calling this), so the row moves even though the old worker is still up.
--     This is how a degrading mailbox's NEXT assignment lands in the degraded
--     band (requirement 4): AssignMailboxWorker recomputes and compares the
--     band on every call, so the very next warmup tick after a lane change
--     picks this branch.
--
-- Keeping this as one atomic upsert (rather than a DELETE + INSERT in the
-- caller) means two workers reassigning the same stranded mailbox converge: the
-- first takes it, the second sees a live, same-band incumbent and adopts that
-- answer.
INSERT INTO mailbox_worker_assignments (mailbox_id, workspace_id, worker_id, band)
SELECT $1, $2, $3, $4 FROM mailboxes WHERE id = $1 AND workspace_id = $2
ON CONFLICT (mailbox_id)
DO UPDATE SET worker_id = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) AND mailbox_worker_assignments.band = EXCLUDED.band
    THEN mailbox_worker_assignments.worker_id
    ELSE EXCLUDED.worker_id
END,
band = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) AND mailbox_worker_assignments.band = EXCLUDED.band
    THEN mailbox_worker_assignments.band
    ELSE EXCLUDED.band
END,
assigned_at = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) AND mailbox_worker_assignments.band = EXCLUDED.band
    THEN mailbox_worker_assignments.assigned_at
    ELSE now()
END
RETURNING worker_id;

-- name: MailboxWorkerAssignmentExists :one
-- Observability only, and deliberately called from ONE branch:
-- AssignMailboxWorker's "no live assignment" path, after
-- GetLiveMailboxWorkerAssignment has already returned pgx.ErrNoRows and
-- before picking a replacement worker. At that point ErrNoRows is ambiguous
-- between "never assigned" (the common first-send case, nothing worth
-- reporting) and "assigned, but the incumbent fell out of the live window"
-- (liveness expiry — previously silent; see the F3 spec's observability
-- requirement). This query resolves that ambiguity with a second, cheap,
-- index-backed lookup that ignores liveness entirely — by the time it runs,
-- the caller already knows the row, if any, is not live.
SELECT EXISTS (
    SELECT 1 FROM mailbox_worker_assignments
    WHERE mailbox_id = $1 AND workspace_id = $2
) AS assignment_exists;
