-- Workspace audit log: an append-only record of security- and
-- governance-relevant actions (sign-ins, membership, credentials, sending
-- state, exports, settings). Written through internal/platform/audit, read
-- through internal/app/audit (owner/admin only). See security.md invariant 82.
--
-- WHY A NEW TABLE rather than the CRM `events` table (migration 000043) or
-- pending_action_audit (000041): `events` is the CRM record timeline — it is
-- read by anyone with crm:read, keyed to contacts/companies/deals, and its
-- foreign keys are ON DELETE SET NULL, i.e. the database itself UPDATEs its
-- rows. pending_action_audit is scoped to one agent approval. Neither can be
-- made append-only without breaking what it already is, and a security log
-- whose readers are "anyone who can see CRM activity" is not a security log.
CREATE TABLE audit_events (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- Who acted. The vocabulary lives in platform/audit.ActorType; a CHECK is
    -- right here (unlike the action column) because an actor kind the reader
    -- does not know how to render is a bug, not an extension.
    actor_type    TEXT NOT NULL CHECK (actor_type IN ('user', 'api_key', 'oauth_client', 'agent', 'system')),
    -- The acting principal's identifier for its type: a user id, api key id,
    -- OAuth client id, agent run/client id, or a system component name.
    -- Text, not UUID, because two of those are not UUIDs.
    actor_id      TEXT NOT NULL DEFAULT '' CHECK (length(actor_id) <= 200),
    -- The human on whose authority the actor acted (the user themself, an api
    -- key's creator, the user who delegated to an agent). NO FOREIGN KEY, on
    -- purpose: an audit row must outlive the user row, and ON DELETE SET NULL
    -- would be an UPDATE that the append-only trigger below refuses.
    actor_user_id UUID,
    -- Stable dotted name (platform/audit.Action). Format-checked but not
    -- enumerated: shipping a new action must not need a migration.
    action        TEXT NOT NULL CHECK (action ~ '^[a-z][a-z_]*(\.[a-z][a-z_]*)+$' AND length(action) <= 100),
    target_type   TEXT NOT NULL DEFAULT '' CHECK (length(target_type) <= 50),
    target_id     TEXT NOT NULL DEFAULT '' CHECK (length(target_id) <= 200),
    ip            INET,
    user_agent    TEXT NOT NULL DEFAULT '' CHECK (length(user_agent) <= 512),
    -- Small, flat, string-valued context (platform/audit.Metadata). Never a
    -- secret, token, password or message body — the writer refuses keys that
    -- look like one, and the size cap bounds what a mistake could leak.
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb
                  CHECK (jsonb_typeof(metadata) = 'object' AND pg_column_size(metadata) <= 4096),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The read path: one workspace, newest first, keyset on (created_at, id).
CREATE INDEX idx_audit_events_workspace_time
    ON audit_events (workspace_id, created_at DESC, id DESC);
-- "What happened to this mailbox / key / campaign" — the target filter.
CREATE INDEX idx_audit_events_workspace_target
    ON audit_events (workspace_id, target_type, target_id, created_at DESC, id DESC)
    WHERE target_id <> '';
-- The retention purge deletes by age across every workspace.
CREATE INDEX idx_audit_events_created ON audit_events (created_at);

-- Append-only, enforced by the database rather than by convention.
--
-- UPDATE is refused unconditionally. DELETE is refused except in two cases:
--   1. The retention purge, which sets inroad.audit_retention_purge = 'on'
--      with SET LOCAL inside its own transaction (PurgeAuditEvents).
--   2. A cascade from deleting the workspace itself (pg_trigger_depth() > 1:
--      the FK's RI trigger is the outer trigger). Refusing that would make a
--      workspace undeletable, and crypto-shredding (invariant 18) depends on
--      workspace deletion working.
-- TRUNCATE is refused by a statement-level trigger.
--
-- What this is NOT: protection against the table owner. The application
-- connects as the role that owns this table, and an owner can disable a
-- trigger or set the GUC. It stops application bugs, a stray UPDATE in a
-- future query, and anything reaching the database through the application's
-- own SQL — not a compromised database credential. REVOKE would not help
-- either, for the same reason: privileges do not bind a table's owner.
CREATE FUNCTION audit_events_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND (current_setting('inroad.audit_retention_purge', true) = 'on'
            OR pg_trigger_depth() > 1) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_events is append-only: % refused', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;

CREATE TRIGGER trg_audit_events_append_only
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION audit_events_append_only();

CREATE TRIGGER trg_audit_events_no_truncate
    BEFORE TRUNCATE ON audit_events
    FOR EACH STATEMENT EXECUTE FUNCTION audit_events_append_only();
