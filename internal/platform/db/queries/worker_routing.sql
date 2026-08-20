-- name: UpsertWorker :exec
-- Heartbeat: register or refresh this worker's row. egress_ip is recorded for
-- observability; last_seen_at drives the live-worker window in the assigner.
INSERT INTO workers (worker_id, egress_ip, last_seen_at)
VALUES ($1, $2, now())
ON CONFLICT (worker_id)
DO UPDATE SET egress_ip = EXCLUDED.egress_ip, last_seen_at = now();

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
SELECT a.worker_id
FROM mailbox_worker_assignments a
JOIN workers w ON w.worker_id = a.worker_id
WHERE a.mailbox_id = $1
  AND a.workspace_id = $2
  AND w.last_seen_at >= @live_since::timestamptz;

-- name: PickLeastLoadedWorker :one
-- The least-loaded LIVE worker (heartbeat at or after live_since). Load is the
-- current assignment count across ALL workspaces — workers are global infra, so
-- balancing is fleet-wide, not per-tenant. Deterministic worker_id tie-break.
-- No live worker => zero rows (the caller falls back to the shared default queue).
SELECT w.worker_id
FROM workers w
WHERE w.last_seen_at >= @live_since::timestamptz
ORDER BY (
    SELECT count(*) FROM mailbox_worker_assignments a WHERE a.worker_id = w.worker_id
) ASC, w.worker_id ASC
LIMIT 1;

-- name: InsertMailboxWorkerAssignment :one
-- Persist an assignment. Self-enforcing tenancy (defense in depth): the row
-- is written ONLY when the mailbox truly belongs to the workspace, so a mismatched
-- (mailbox, workspace) pair inserts zero rows and RETURNING yields pgx.ErrNoRows —
-- the caller maps that to a cross-tenant rejection.
--
-- On a mailbox_id conflict the row is claimed for the incoming worker ONLY if the
-- incumbent has gone silent (last_seen_at < live_since, or no workers row at all);
-- otherwise the incumbent's worker_id is kept and returned. That single rule serves
-- both callers:
--
--   * concurrent first-send race — both racers see a LIVE incumbent (whichever
--     inserted first), so the existing row wins and both resolve to the same
--     queue, exactly as before.
--   * reassignment after a worker died — the incumbent is not live, so the row
--     moves to the caller's freshly-picked live worker instead of being pinned
--     to a queue nobody consumes.
--
-- Keeping this as one atomic upsert (rather than a DELETE + INSERT in the caller)
-- means two workers reassigning the same stranded mailbox converge: the first
-- takes it, the second sees a live incumbent and adopts that answer.
INSERT INTO mailbox_worker_assignments (mailbox_id, workspace_id, worker_id)
SELECT $1, $2, $3 FROM mailboxes WHERE id = $1 AND workspace_id = $2
ON CONFLICT (mailbox_id)
DO UPDATE SET worker_id = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) THEN mailbox_worker_assignments.worker_id
    ELSE EXCLUDED.worker_id
END,
assigned_at = CASE
    WHEN EXISTS (
        SELECT 1 FROM workers w
        WHERE w.worker_id = mailbox_worker_assignments.worker_id
          AND w.last_seen_at >= @live_since::timestamptz
    ) THEN mailbox_worker_assignments.assigned_at
    ELSE now()
END
RETURNING worker_id;
