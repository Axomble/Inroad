-- Restore the exact definition from 20260923105934_audit_events.up.sql.
CREATE OR REPLACE FUNCTION audit_events_append_only() RETURNS trigger
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
