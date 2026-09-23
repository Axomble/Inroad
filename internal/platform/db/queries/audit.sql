-- name: InsertAuditEvent :exec
-- The ONLY write to audit_events. Called through platform/audit (Insert /
-- PgRecorder), which validates the event first; a tx-bound *gen.Queries makes
-- the row commit or roll back with the action it records.
INSERT INTO audit_events (
    workspace_id, actor_type, actor_id, actor_user_id, action,
    target_type, target_id, ip, user_agent, metadata
) VALUES (
    @workspace_id, @actor_type, @actor_id, sqlc.narg(actor_user_id), @action,
    @target_type, @target_id, sqlc.narg(ip), @user_agent, @metadata
);

-- name: ListAuditEvents :many
-- The owner/admin audit viewer, newest first, keyset-paged.
--
-- Every filter uses the "empty means any" sentinel shape ListTaskDeadLetters
-- uses, so one statement serves every combination. The action filter is a
-- dotted PREFIX on a segment boundary: 'campaign' matches campaign.paused but
-- not campaigns.x, and a full name matches only itself. starts_with() rather
-- than LIKE, so '_' in an action name is never a wildcard.
--
-- The workspace pin is FIRST and unconditional, outside every caller-supplied
-- guard (the seek included): a tenant filter a caller-supplied value can switch
-- off is not a tenant filter.
--
-- actor_email is a LEFT JOIN so a row whose user has since been deleted still
-- lists (with a NULL email) — the reason actor_user_id carries no foreign key.
SELECT
    e.id, e.workspace_id, e.actor_type, e.actor_id, e.actor_user_id, e.action,
    e.target_type, e.target_id, e.ip, e.user_agent, e.metadata, e.created_at,
    u.email AS actor_email
FROM audit_events e
LEFT JOIN users u ON u.id = e.actor_user_id
WHERE e.workspace_id = @workspace_id
  AND (@action_prefix::text = ''
       OR e.action = @action_prefix::text
       OR starts_with(e.action, @action_prefix::text || '.'))
  AND (@actor_type::text = '' OR e.actor_type = @actor_type::text)
  AND (@actor_id::text = '' OR e.actor_id = @actor_id::text)
  AND (sqlc.narg(actor_user_id)::uuid IS NULL OR e.actor_user_id = sqlc.narg(actor_user_id)::uuid)
  AND (@target_type::text = '' OR e.target_type = @target_type::text)
  AND (@target_id::text = '' OR e.target_id = @target_id::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.created_at >= sqlc.narg(since)::timestamptz)
  AND (sqlc.narg(until)::timestamptz IS NULL OR e.created_at < sqlc.narg(until)::timestamptz)
  AND (@seek::bool = false
       OR (e.created_at, e.id) < (@cursor_time::timestamptz, @cursor_id::uuid))
ORDER BY e.created_at DESC, e.id DESC
LIMIT @page_limit;

-- name: EnableAuditRetentionPurge :exec
-- Opens the append-only trigger's one door for the REST OF THE CURRENT
-- TRANSACTION (is_local = true). Must run inside the same transaction as
-- PurgeAuditEvents; on its own it is a no-op that expires at commit.
SELECT set_config('inroad.audit_retention_purge', 'on', true);

-- name: PurgeAuditEvents :one
-- Retention sweep, by age alone across every workspace, in batches of 5000
-- like the other maintenance purges. Retention is OFF unless the operator sets
-- INROAD_AUDIT_RETENTION_DAYS; the `@retention_days > 0` guard means a zero
-- that slipped past the caller deletes nothing rather than everything older
-- than "now".
WITH deleted AS (
    DELETE FROM audit_events
    WHERE id IN (
        SELECT id FROM audit_events
        WHERE @retention_days::int > 0
          AND created_at < now() - make_interval(days => @retention_days::int)
        ORDER BY created_at
        LIMIT 5000
    )
    RETURNING 1
)
SELECT count(*)::bigint AS deleted_rows FROM deleted;
