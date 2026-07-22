-- name: EnrollListMembers :many
-- One active enrollment per list member for the campaign. next_due_at is
-- staggered per contact at insert time (row_number * 2s) so a launch of N
-- contacts doesn't burst the mailbox and the sweeper won't collapse the pacing
-- by treating them all as due at once. The spread is capped at 30 minutes so a
-- large list can't push the last contact hours out. ON CONFLICT keeps re-launch
-- idempotent (a contact already enrolled is left untouched).
-- RETURNING next_due_at alongside id so the launcher enqueues each advance at
-- the exact time the DB assigned: Postgres does NOT guarantee RETURNING row
-- order matches the window ORDER BY, so recomputing the stagger from the Go
-- slice index would drift the scheduled task off its enrollment's due time.
INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, next_due_at)
SELECT cam.workspace_id, cam.id, lm.contact_id,
       now() + LEAST(
         (row_number() OVER (ORDER BY lm.contact_id) - 1) * interval '2 seconds',
         interval '30 minutes')
FROM campaigns cam
JOIN list_members lm ON lm.list_id = cam.list_id
JOIN contacts ct ON ct.id = lm.contact_id
WHERE cam.id = $1 AND cam.workspace_id = $2
ON CONFLICT (campaign_id, contact_id) DO NOTHING
RETURNING id, next_due_at;

-- name: GetEnrollment :one
SELECT * FROM sequence_enrollments WHERE id = $1 AND workspace_id = $2;

-- name: AdvanceEnrollmentStep :exec
-- Record a successful step send and schedule the next: bump current_step,
-- stamp last_sent_at (the cadence reference point), set the next due time,
-- keep status active. Reset cap_deferrals to 0: a successful send clears the
-- run of cap-defers, so the counter tracks CONSECUTIVE defers since the last
-- send (bounded by maxCapDeferrals), not a lifetime total that would wrongly
-- fail a long, healthy campaign that occasionally brushes the daily cap.
UPDATE sequence_enrollments
SET current_step = $3, last_sent_at = now(), next_due_at = $4, cap_deferrals = 0
WHERE id = $1 AND workspace_id = $2;

-- name: CompleteEnrollment :exec
-- Final step sent: bump current_step, stamp last_sent_at, mark completed and
-- clear next_due_at (drops the row out of the partial due index). Reset
-- cap_deferrals to 0 on success for the same reason as AdvanceEnrollmentStep.
UPDATE sequence_enrollments
SET current_step = $3, last_sent_at = now(), status = 'completed',
    completed_at = now(), next_due_at = NULL, cap_deferrals = 0
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

-- name: IncrementEnrollmentCapDeferrals :one
-- Bump the cap-deferral counter and return the new value, mirroring
-- IncrementSendAttempts on the direct-send path. The advance handler uses it to
-- bail out of the cap-defer loop (stop 'failed') when a mailbox cap is never
-- clearing, so a mis-set cap can't re-enqueue an enrollment forever.
UPDATE sequence_enrollments SET cap_deferrals = cap_deferrals + 1
WHERE id = $1 AND workspace_id = $2
RETURNING cap_deferrals;

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
