-- name: ListBranchesByCampaign :many
-- Every router in the campaign, workspace-pinned. The send path reads this once
-- per advance: an empty result is the linear campaign, which then takes exactly
-- the pre-branching code path.
SELECT * FROM sequence_step_branches
WHERE campaign_id = $1 AND workspace_id = $2
ORDER BY step_id;

-- name: UpsertBranch :one
-- Create or replace the router on one step. workspace_id and campaign_id are
-- written from the caller's pinned values, and the composite FKs refuse a step
-- (source or target) that is not in that campaign, so a foreign id cannot be
-- smuggled in even if the service's own check were skipped. The ON CONFLICT
-- update is pinned on workspace_id as well: a step id belonging to another tenant
-- updates nothing and returns no row. updated_at is not written here: a trigger
-- advances it on every insert and update (migration
-- 20260923161044_step_branch_updated_at_advances), because it is the branch's
-- concurrency token.
INSERT INTO sequence_step_branches (step_id, workspace_id, campaign_id, condition, within_days,
                                    reply_label_key, yes_step_id, no_step_id)
VALUES ($1, $2, $3, $4, sqlc.narg(within_days), sqlc.narg(reply_label_key),
        sqlc.narg(yes_step_id), sqlc.narg(no_step_id))
ON CONFLICT (step_id) DO UPDATE
SET condition = EXCLUDED.condition, within_days = EXCLUDED.within_days,
    reply_label_key = EXCLUDED.reply_label_key, yes_step_id = EXCLUDED.yes_step_id,
    no_step_id = EXCLUDED.no_step_id
WHERE sequence_step_branches.workspace_id = EXCLUDED.workspace_id
  AND sequence_step_branches.campaign_id = EXCLUDED.campaign_id
RETURNING *;

-- name: InsertBranchIfAbsent :one
-- Create the router on one step only if the step has none: the "expect no
-- branch" precondition. No row returned means one already exists (the caller
-- reads it with GetBranch to report it). Atomic on its own via the primary key,
-- so two concurrent creates cannot both succeed even without the graph lock.
INSERT INTO sequence_step_branches (step_id, workspace_id, campaign_id, condition, within_days,
                                    reply_label_key, yes_step_id, no_step_id)
VALUES ($1, $2, $3, $4, sqlc.narg(within_days), sqlc.narg(reply_label_key),
        sqlc.narg(yes_step_id), sqlc.narg(no_step_id))
ON CONFLICT (step_id) DO NOTHING
RETURNING *;

-- name: UpdateBranchIfUnchanged :one
-- Replace the router on one step only if its updated_at still equals the value
-- the client last read: the "expect this version" precondition. No row returned
-- means the branch changed or is gone. Compared at full (microsecond) precision;
-- the trigger guarantees every write moves the value. Pinned on workspace_id and
-- campaign_id.
UPDATE sequence_step_branches
SET condition = sqlc.arg(condition), within_days = sqlc.narg(within_days),
    reply_label_key = sqlc.narg(reply_label_key), yes_step_id = sqlc.narg(yes_step_id),
    no_step_id = sqlc.narg(no_step_id)
WHERE step_id = sqlc.arg(step_id) AND workspace_id = sqlc.arg(workspace_id)
  AND campaign_id = sqlc.arg(campaign_id)
  AND updated_at = sqlc.arg(expected_updated_at)::timestamptz
RETURNING *;

-- name: GetBranch :one
-- One step's router, workspace- and campaign-pinned. Read inside a refused
-- precondition's transaction, so the 409 carries the branch that won.
SELECT * FROM sequence_step_branches
WHERE step_id = $1 AND campaign_id = $2 AND workspace_id = $3;

-- name: DeleteBranch :exec
-- Remove a step's router, returning it to linear fall-through. Pinned on
-- workspace_id and campaign_id.
DELETE FROM sequence_step_branches
WHERE step_id = $1 AND campaign_id = $2 AND workspace_id = $3;

-- name: DeleteBranchIfUnchanged :execrows
-- Remove a step's router only if its updated_at still equals the value the
-- client last read. Zero rows means it changed or was already removed.
DELETE FROM sequence_step_branches
WHERE step_id = $1 AND campaign_id = $2 AND workspace_id = $3
  AND updated_at = sqlc.arg(expected_updated_at)::timestamptz;

-- name: LockCampaignGraph :one
-- Serializes every write that changes a campaign's routing graph (branch
-- upsert/delete, step delete, reorder), so the save-time cycle check sees the
-- graph it is actually committing into — two edits that are each acyclic alone
-- can form a cycle together. FOR NO KEY UPDATE rather than FOR UPDATE: it still
-- conflicts with itself, but not with the FOR KEY SHARE lock every sends insert
-- takes on its campaign FK, so a graph edit never stalls delivery.
SELECT id FROM campaigns WHERE id = $1 AND workspace_id = $2 FOR NO KEY UPDATE;

-- name: ReplyLabelStopsEnrollment :one
-- Whether the workspace's reply label with this key stops the enrollment.
-- Save-time validation only: no row (pgx.ErrNoRows) means the label does not
-- exist and a branch naming it could never match; true means a reply with that
-- label stops the sequence before any branch can route it, so a branch naming it
-- could never fire either.
SELECT stops_enrollment FROM reply_labels WHERE workspace_id = $1 AND key = $2;

-- name: CampaignTrackingEnabled :one
-- Whether the campaign rewrites links and embeds the open pixel. Save-time
-- validation for open/click branches, which have no evidence to read otherwise.
SELECT tracking_enabled FROM campaigns WHERE id = $1 AND workspace_id = $2;

-- name: FirstHumanTrackingEventAt :one
-- The earliest HUMAN open or click of one send, at or before window_end. It reads
-- the stored bot verdict (NOT is_machine) — the same definition CountHumanOpens
-- reports — rather than deriving its own, so a branch and the open rate can
-- never disagree about the same contact (docs/security.md invariant 84). Served
-- by idx_tracking_send_recent (send_id, kind, is_machine, created_at).
SELECT created_at FROM tracking_events
WHERE send_id = $1 AND workspace_id = $2 AND kind = $3 AND NOT is_machine
  AND created_at <= sqlc.arg(window_end)::timestamptz
ORDER BY created_at ASC
LIMIT 1;

-- name: FirstInboundReplyAt :one
-- The earliest inbound reply from this contact on this campaign that arrived
-- inside [window_start, window_end]. created_at (when WE ingested it), not
-- occurred_at: the latter is the sender's own Date header, which they control.
--
-- label_key '' means "any human reply": a message whose label is automated
-- (out-of-office, auto-reply) is not a reply from a person and does not count. A
-- label the workspace has since deleted falls back to the builtin automated keys,
-- the same degradation the inbox dispatch applies. A non-empty label_key matches
-- that key exactly, automated or not — branching on an out-of-office is a
-- legitimate thing to want.
SELECT m.created_at FROM inbox_messages m
JOIN inbox_threads t ON t.id = m.thread_id AND t.workspace_id = m.workspace_id
LEFT JOIN reply_labels rl ON rl.workspace_id = m.workspace_id AND rl.key = m.reply_class
WHERE m.workspace_id = $1 AND t.campaign_id = $2 AND t.contact_id = $3
  AND m.direction = 'inbound'
  AND m.created_at >= sqlc.arg(window_start)::timestamptz
  AND m.created_at <= sqlc.arg(window_end)::timestamptz
  AND CASE WHEN sqlc.arg(label_key)::text = ''
           THEN NOT COALESCE(rl.is_automated, m.reply_class IN ('auto_reply', 'out_of_office'))
           ELSE m.reply_class = sqlc.arg(label_key)::text
      END
ORDER BY m.created_at ASC
LIMIT 1;

-- name: StepSendCreatedAt :one
-- When a (deterministically-id'd) step send row was first created. The send
-- path's cycle backstop compares the routed step's row with the CURRENT step's
-- own row: a routed step created before the step the contact is on was visited
-- EARLIER on this path, i.e. the graph now loops. Workspace-pinned.
SELECT created_at FROM sends WHERE id = $1 AND workspace_id = $2;
