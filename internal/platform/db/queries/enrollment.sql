-- name: EnrollListMembers :many
-- One active enrollment per list member for the campaign. next_due_at is a
-- placeholder (now()): the launcher immediately re-stamps every row through the
-- cadence engine (SetEnrollmentDueBatch), which spreads the batch across the
-- campaign's send window off the clock grid. This used to stagger here as
-- row_number * 2s, which put every send of a launch on a uniform 2-second grid —
-- one of the cheapest bulk-sender signals a receiving MTA can key on.
-- ON CONFLICT keeps re-launch idempotent (a contact already enrolled is left
-- untouched). RETURNING order no longer matters: Go assigns each enrollment its
-- own instant explicitly and writes it back, rather than trying to keep two
-- independent computations of the stagger in agreement.
INSERT INTO sequence_enrollments (workspace_id, campaign_id, contact_id, next_due_at)
SELECT cam.workspace_id, cam.id, lm.contact_id, now()
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
-- Guarded on status='active' (like StopEnrollment): the send path splits
-- delivery (MarkStepDelivered) from this cursor advance into separate
-- transactions, so a concurrent reply/bounce/unsubscribe stop can land between
-- them. A stop is terminal and wins — this UPDATE then matches 0 rows and is a
-- safe no-op (:exec surfaces no error), leaving the enrollment 'stopped' rather
-- than clobbering it back to active.
UPDATE sequence_enrollments
SET current_step = $3, last_sent_at = now(), next_due_at = $4, cap_deferrals = 0
WHERE id = $1 AND workspace_id = $2 AND status = 'active';

-- name: CompleteEnrollment :exec
-- Final step sent: bump current_step, stamp last_sent_at, mark completed and
-- clear next_due_at (drops the row out of the partial due index). Reset
-- cap_deferrals to 0 on success for the same reason as AdvanceEnrollmentStep.
-- Guarded on status='active' for the same reason: a stop that lands between the
-- last step's MarkStepDelivered and this complete is terminal and must win, so
-- a stopped enrollment is NOT overwritten to 'completed' (0 rows, no-op).
UPDATE sequence_enrollments
SET current_step = $3, last_sent_at = now(), status = 'completed',
    completed_at = now(), next_due_at = NULL, cap_deferrals = 0
WHERE id = $1 AND workspace_id = $2 AND status = 'active';

-- name: StopEnrollment :exec
-- The single stop entry point (unsubscribe now; reply/bounce deferred). Only
-- stops an active enrollment so a completed one is never reopened as stopped.
UPDATE sequence_enrollments
SET status = 'stopped', stop_reason = $3::text, stopped_at = now(), next_due_at = NULL
WHERE id = $1 AND workspace_id = $2 AND status = 'active';

-- name: SetEnrollmentReplyClass :exec
-- Store the classified reply (class/source/confidence + when) on the
-- enrollment WITHOUT touching status. Used on its own for automated replies
-- (auto_reply/out_of_office), and alongside StopEnrollment when a reply also
-- halts the sequence (replied/unsubscribed). Workspace-pinned so a caller
-- can't tag another tenant's enrollment.
UPDATE sequence_enrollments
SET reply_class = $3, reply_source = $4, reply_confidence = $5, replied_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: SetEnrollmentDueBatch :batchexec
-- Stamps cadence-computed due times onto a whole launch: one statement per
-- enrollment, pipelined as a single pgx batch, so a launch of N contacts costs
-- one round trip instead of N. workspace_id is pinned and status='active'
-- guarded for the same reasons as SetEnrollmentDue — a foreign id or an
-- already-stopped enrollment matches zero rows instead of being re-stamped.
UPDATE sequence_enrollments SET next_due_at = $3
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

-- name: CountEnrollmentsByStopReason :many
-- Terminal (stopped) enrollments grouped by stop_reason, for the per-campaign
-- reply/bounce/unsubscribe metrics rollup. Distinct from
-- CountEnrollmentsByStatus, which groups by lifecycle status
-- (active/completed/stopped) for the detail view's enrollment-count widget.
SELECT stop_reason, count(*) AS n FROM sequence_enrollments
WHERE campaign_id = $1 AND workspace_id = $2 AND status = 'stopped'
GROUP BY stop_reason;

-- name: ListCampaignEnrollments :many
-- Per-contact reply status for a campaign's enrollments, joined to the contact
-- for the display email/name. Workspace-pinned on the enrollment (defense in
-- depth alongside the service's ownership check) so a cross-tenant campaign id
-- yields no rows rather than leaking another tenant's contacts. Ordered by most
-- recently replied first (NULLS LAST keeps never-replied rows after replied
-- ones), then email for a stable page order. Paginated via LIMIT/OFFSET.
SELECT c.email, c.first_name, e.status, e.reply_class, e.reply_source, e.replied_at
FROM sequence_enrollments e
JOIN contacts c ON c.id = e.contact_id
WHERE e.campaign_id = $1 AND e.workspace_id = $2
ORDER BY e.replied_at DESC NULLS LAST, c.email ASC
LIMIT $3 OFFSET $4;

-- name: ListDueEnrollments :many
-- Sweeper hot path: active enrollments whose next_due_at passed the reconcile
-- window. Served by the partial idx_enrollments_due. Capped so one sweep tick
-- can't monopolize the worker.
SELECT id, workspace_id FROM sequence_enrollments
WHERE status = 'active' AND next_due_at IS NOT NULL
  AND next_due_at < now() - interval '5 minutes'
ORDER BY next_due_at ASC
LIMIT 500;
