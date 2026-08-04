-- name: CreateCampaign :one
INSERT INTO campaigns (workspace_id, name, mailbox_id, list_id, subject, body_text, body_html)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: GetCampaign :one
SELECT * FROM campaigns WHERE id = $1 AND workspace_id = $2;
-- name: ListCampaigns :many
SELECT * FROM campaigns WHERE workspace_id = $1 ORDER BY created_at DESC;
-- name: SetCampaignStatus :exec
UPDATE campaigns SET status = $3, launched_at = COALESCE(launched_at, $4)
WHERE id = $1 AND workspace_id = $2;
-- name: SetCampaignTracking :exec
UPDATE campaigns SET tracking_enabled = $3 WHERE id = $1 AND workspace_id = $2;
-- name: SetCampaignRotationMode :exec
-- How a contact is assigned a mailbox from the campaign's sender pool. Validated
-- at the boundary against the rotation package's mode constants, which mirror the
-- column's CHECK constraint.
UPDATE campaigns SET rotation_mode = $3 WHERE id = $1 AND workspace_id = $2;

-- name: SetCampaignTimezone :exec
-- The IANA zone every send window on the campaign is interpreted in. Validated
-- at the boundary with time.LoadLocation before it reaches here.
UPDATE campaigns SET timezone = $3 WHERE id = $1 AND workspace_id = $2;

-- name: ListSendWindows :many
-- A campaign's whole weekly schedule, ordered so the cadence engine receives
-- each day's intervals already sorted and can skip re-sorting.
SELECT weekday, start_minute, end_minute FROM campaign_send_windows
WHERE campaign_id = $1 AND workspace_id = $2
ORDER BY weekday, start_minute;

-- name: DeleteSendWindows :exec
-- Clears the schedule ahead of a full replace. Paired with CreateSendWindows in
-- one transaction so a campaign is never observed window-less (an empty week
-- means "no valid send instant exists" to the engine).
DELETE FROM campaign_send_windows WHERE campaign_id = $1 AND workspace_id = $2;

-- name: CreateSendWindows :batchexec
-- One interval per call, pipelined as a single batch so replacing a whole week
-- costs one round trip. Overlaps are rejected by the send_window_no_overlap
-- exclusion constraint rather than trusted from the caller, so a bad batch fails
-- inside the replace transaction and rolls back whole.
INSERT INTO campaign_send_windows (workspace_id, campaign_id, weekday, start_minute, end_minute)
VALUES ($1, $2, $3, $4, $5);

-- name: CountSendsByStatus :many
-- Workspace-scoped for defense in depth: even if a caller supplies a
-- campaign id from another tenant, the workspace filter forces a 0-row
-- result rather than leaking counts across tenants.
SELECT status, count(*) AS n FROM sends
WHERE campaign_id = $1 AND workspace_id = $2
GROUP BY status;

-- name: SetCampaignDailyLimit :exec
-- The campaign-wide ceiling on sends per UTC day; NULL clears it. Validated at
-- the boundary (>= 1) against the same rule the column's CHECK enforces, so a
-- rejected value is a 422 rather than a constraint violation surfacing as a 500.
UPDATE campaigns SET daily_limit = $3 WHERE id = $1 AND workspace_id = $2;

-- name: RenameCampaign :one
-- Rename is allowed at any lifecycle status; the service validates the name
-- (min=1,max=200, mirroring Create) before this runs.
UPDATE campaigns SET name = $3 WHERE id = $1 AND workspace_id = $2 RETURNING *;

-- name: DeleteDraftCampaign :execrows
-- Guarded on status='draft' in SQL as defense in depth; the service re-checks
-- first for a typed 409 (ErrNotDraft). Run only after the caller has deleted
-- the draft's dependents (send windows, campaign_senders, sequence_steps,
-- sequence_enrollments) in the same transaction -- this does NOT rely on FK
-- cascade, even though every one of those FKs is ON DELETE CASCADE.
DELETE FROM campaigns WHERE id = $1 AND workspace_id = $2 AND status = 'draft';

-- name: DeleteCampaignSendersByCampaign :exec
-- Unconditional delete of every pool member, for DeleteDraftCampaign's
-- transaction. Distinct from DeleteCampaignSendersExcept (sender.sql), which
-- keeps a caller-supplied subset for a pool replace.
DELETE FROM campaign_senders WHERE campaign_id = $1 AND workspace_id = $2;

-- name: DeleteStepsByCampaign :exec
-- Unconditional delete of every sequence step, for DeleteDraftCampaign's
-- transaction. Distinct from DeleteStep (sequencestep.sql), which removes one
-- step by id.
DELETE FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2;

-- name: DeleteEnrollmentsByCampaign :exec
-- A draft campaign has never been launched so this is normally a no-op, but a
-- draft can carry enrollment rows left over from build tooling or a future
-- re-draft path; deleted explicitly rather than assumed empty.
DELETE FROM sequence_enrollments WHERE campaign_id = $1 AND workspace_id = $2;
