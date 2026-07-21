-- name: EnrollListMembers :many
-- One active enrollment per list member for the campaign. next_due_at starts
-- at now(); the launcher re-stamps it with a per-contact stagger and enqueues
-- the step-1 advance. ON CONFLICT keeps re-launch idempotent (a contact already
-- enrolled is left untouched).
INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, next_due_at)
SELECT cam.workspace_id, cam.id, lm.contact_id, now()
FROM campaigns cam
JOIN list_members lm ON lm.list_id = cam.list_id
JOIN contacts ct ON ct.id = lm.contact_id
WHERE cam.id = $1 AND cam.workspace_id = $2
ON CONFLICT (campaign_id, contact_id) DO NOTHING
RETURNING id;

-- name: GetEnrollment :one
SELECT * FROM sequence_enrollments WHERE id = $1 AND workspace_id = $2;

-- name: AdvanceEnrollmentStep :exec
-- Record a successful step send and schedule the next: bump current_step,
-- stamp last_sent_at (the cadence reference point), set the next due time,
-- keep status active.
UPDATE sequence_enrollments
SET current_step = $3, last_sent_at = now(), next_due_at = $4
WHERE id = $1 AND workspace_id = $2;

-- name: CompleteEnrollment :exec
-- Final step sent: bump current_step, stamp last_sent_at, mark completed and
-- clear next_due_at (drops the row out of the partial due index).
UPDATE sequence_enrollments
SET current_step = $3, last_sent_at = now(), status = 'completed',
    completed_at = now(), next_due_at = NULL
WHERE id = $1 AND workspace_id = $2;

-- name: StopEnrollment :exec
-- The single stop entry point (unsubscribe now; reply/bounce deferred). Only
-- stops an active enrollment so a completed one is never reopened as stopped.
UPDATE sequence_enrollments
SET status = 'stopped', stop_reason = $3::text, stopped_at = now(), next_due_at = NULL
WHERE id = $1 AND workspace_id = $2 AND status = 'active';

-- name: SetEnrollmentDue :exec
-- Re-stamp the next due time for an active enrollment (launch stagger + sweeper
-- reconcile). No-op on non-active rows.
UPDATE sequence_enrollments
SET next_due_at = $3
WHERE id = $1 AND workspace_id = $2 AND status = 'active';

-- name: SetThreadRoot :exec
-- Store step 1's Message-ID as the thread root for the References chain on
-- later steps. Set once — only while still empty.
UPDATE sequence_enrollments
SET thread_root_id = $3
WHERE id = $1 AND workspace_id = $2 AND thread_root_id = '';

-- name: CountEnrollmentsByStatus :many
SELECT status, count(*) AS n FROM sequence_enrollments
WHERE campaign_id = $1 AND workspace_id = $2 GROUP BY status;

-- name: ListDueEnrollments :many
-- Sweeper hot path: active enrollments whose next_due_at passed the reconcile
-- window. Served by the partial idx_enrollments_due. Capped so one sweep tick
-- can't monopolize the worker.
SELECT id, workspace_id FROM sequence_enrollments
WHERE status = 'active' AND next_due_at IS NOT NULL
  AND next_due_at < now() - interval '5 minutes'
ORDER BY next_due_at ASC
LIMIT 500;
