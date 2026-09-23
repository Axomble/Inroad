DROP TRIGGER IF EXISTS trg_audit_events_no_truncate ON audit_events;
DROP TRIGGER IF EXISTS trg_audit_events_append_only ON audit_events;
DROP TABLE IF EXISTS audit_events;
DROP FUNCTION IF EXISTS audit_events_append_only();
