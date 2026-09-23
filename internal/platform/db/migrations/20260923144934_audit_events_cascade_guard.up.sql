-- Tighten audit_events' append-only trigger (migration 20260923105934).
--
-- Its cascade exception was `pg_trigger_depth() > 1`: ANY delete issued from
-- inside another trigger passed, which is broader than the one case it exists
-- for. It now also requires that the row's workspace no longer exists — true
-- when, and only when, the delete is the ON DELETE CASCADE from deleting that
-- workspace (the RI trigger runs after the workspace row is gone). A future
-- trigger that deletes audit rows while their workspace lives is refused like
-- any other DELETE.
CREATE OR REPLACE FUNCTION audit_events_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE'
       AND (current_setting('inroad.audit_retention_purge', true) = 'on'
            OR (pg_trigger_depth() > 1
                AND NOT EXISTS (SELECT 1 FROM workspaces WHERE id = OLD.workspace_id))) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'audit_events is append-only: % refused', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$;
