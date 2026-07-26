-- Warmup control-plane persistence (spec §3). Every tenant query is
-- workspace_id-pinned (belt-and-braces on the unguessable mailbox/workspace
-- UUIDs), mirroring queries/mailbox.sql. The send/receipt/thread/health query
-- surface belongs to later worker steps and is intentionally not here yet.

-- name: UpsertWarmupParticipant :one
-- Enable warmup for a mailbox or update its ramp settings. On re-enable the row
-- is flipped back to enabled=true. Self-enforcing tenancy (defense in depth): the
-- base INSERT is an INSERT ... SELECT that emits a row ONLY when the mailbox truly
-- belongs to the workspace, so a first upsert with a foreign (mailbox, workspace)
-- pair inserts zero rows and RETURNING yields pgx.ErrNoRows — never binding another
-- tenant's mailbox into this workspace. The ON CONFLICT UPDATE is likewise
-- workspace-pinned, so a cross-workspace collision on an existing row updates
-- nothing and returns no row. The caller maps that ErrNoRows to a domain sentinel.
INSERT INTO warmup_participants (
    mailbox_id, workspace_id,
    start_volume, max_volume, ramp_increment, reply_rate
)
SELECT $1, $2, $3, $4, $5, $6
FROM mailboxes WHERE id = $1 AND workspace_id = $2
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

-- Day-boundary convention: the daily-stats reads below anchor their windows on
-- CURRENT_DATE. The DB session runs in UTC, so "today"/"last N days" are UTC-day
-- boundaries, not any recipient-local day. The future stats WRITER (C4) MUST
-- aggregate on the same UTC boundary so writes and reads agree. (Engagement
-- waking-hours scheduling uses recipient-local time separately; daily_stats is
-- strictly UTC.)

-- name: GetWarmupDailyStats :many
-- One mailbox's last 30 UTC days of counters, oldest first, for the detail series.
SELECT * FROM warmup_daily_stats
WHERE mailbox_id = $1 AND workspace_id = $2
  AND day >= CURRENT_DATE - 29
ORDER BY day ASC;

-- name: GetWarmupPlacementRates7d :many
-- Per-mailbox inbox/spam/received sums over the trailing 7 UTC days for the
-- overview placement rates. Grouped by mailbox, scoped to one workspace.
SELECT
    mailbox_id,
    COALESCE(SUM(inbox), 0)::bigint    AS inbox,
    COALESCE(SUM(spam), 0)::bigint     AS spam,
    COALESCE(SUM(received), 0)::bigint AS received
FROM warmup_daily_stats
WHERE workspace_id = $1 AND day >= CURRENT_DATE - 6
GROUP BY mailbox_id;

-- name: GetWarmupSentToday :one
-- Today's (UTC) sent count for one mailbox. Aggregated so a missing day row
-- yields 0.
SELECT COALESCE(SUM(sent), 0)::int AS sent
FROM warmup_daily_stats
WHERE mailbox_id = $1 AND workspace_id = $2
  AND day = CURRENT_DATE;
