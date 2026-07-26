-- Warmup control-plane persistence (spec §3). Every tenant query is
-- workspace_id-pinned (belt-and-braces on the unguessable mailbox/workspace
-- UUIDs), mirroring queries/mailbox.sql. The send/receipt/thread/health query
-- surface belongs to later worker steps and is intentionally not here yet.

-- name: UpsertWarmupParticipant :one
-- Enable warmup for a mailbox or update its ramp settings. On re-enable the row
-- is flipped back to enabled=true. The ON CONFLICT UPDATE is workspace-pinned so
-- a cross-workspace mailbox_id collision updates nothing and returns no row.
INSERT INTO warmup_participants (
    mailbox_id, workspace_id,
    start_volume, max_volume, ramp_increment, reply_rate
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (mailbox_id) DO UPDATE SET
    enabled        = true,
    start_volume   = EXCLUDED.start_volume,
    max_volume     = EXCLUDED.max_volume,
    ramp_increment = EXCLUDED.ramp_increment,
    reply_rate     = EXCLUDED.reply_rate,
    updated_at     = now()
WHERE warmup_participants.workspace_id = $2
RETURNING *;

-- name: GetWarmupParticipant :one
SELECT * FROM warmup_participants
WHERE mailbox_id = $1 AND workspace_id = $2;

-- name: ListWarmupParticipants :many
SELECT * FROM warmup_participants
WHERE workspace_id = $1
ORDER BY created_at DESC;

-- name: DisableWarmupParticipant :execrows
-- Disabling deletes the row (spec §10: DELETE /mailboxes/{id}/warmup -> 204).
DELETE FROM warmup_participants
WHERE mailbox_id = $1 AND workspace_id = $2;

-- name: CountEnabledParticipants :one
SELECT count(*) FROM warmup_participants
WHERE workspace_id = $1 AND enabled;

-- name: GetWarmupDailyStats :many
-- One mailbox's last 30 days of counters, oldest first, for the detail series.
SELECT * FROM warmup_daily_stats
WHERE mailbox_id = $1 AND workspace_id = $2
  AND day >= CURRENT_DATE - 29
ORDER BY day ASC;

-- name: GetWarmupPlacementRates7d :many
-- Per-mailbox inbox/spam/received sums over the trailing 7 days for the overview
-- placement rates. Grouped by mailbox, scoped to one workspace.
SELECT
    mailbox_id,
    COALESCE(SUM(inbox), 0)::bigint    AS inbox,
    COALESCE(SUM(spam), 0)::bigint     AS spam,
    COALESCE(SUM(received), 0)::bigint AS received
FROM warmup_daily_stats
WHERE workspace_id = $1 AND day >= CURRENT_DATE - 6
GROUP BY mailbox_id;

-- name: GetWarmupSentToday :one
-- Today's sent count for one mailbox. Aggregated so a missing day row yields 0.
SELECT COALESCE(SUM(sent), 0)::int AS sent
FROM warmup_daily_stats
WHERE mailbox_id = $1 AND workspace_id = $2
  AND day = CURRENT_DATE;
