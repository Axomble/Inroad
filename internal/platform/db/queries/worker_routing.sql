-- name: UpsertWorker :exec
-- Heartbeat: register or refresh this worker's row. egress_ip is recorded for
-- observability; last_seen_at drives the live-worker window in the assigner.
INSERT INTO workers (worker_id, egress_ip, last_seen_at)
VALUES ($1, $2, now())
ON CONFLICT (worker_id)
DO UPDATE SET egress_ip = EXCLUDED.egress_ip, last_seen_at = now();

-- name: GetMailboxWorkerAssignment :one
-- Existing assignment for a mailbox (workspace-pinned: tenant data). A foreign
-- workspace_id matches zero rows.
SELECT worker_id FROM mailbox_worker_assignments
WHERE mailbox_id = $1 AND workspace_id = $2;

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
-- Persist a first assignment. Idempotent under a concurrent first-send race: on
-- a mailbox_id conflict the existing row wins and its worker_id is returned, so
-- both racers resolve to the same queue.
INSERT INTO mailbox_worker_assignments (mailbox_id, workspace_id, worker_id)
VALUES ($1, $2, $3)
ON CONFLICT (mailbox_id)
DO UPDATE SET worker_id = mailbox_worker_assignments.worker_id
RETURNING worker_id;
