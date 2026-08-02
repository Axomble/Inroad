-- Restore the index exactly as 000034 created it, so a rollback lands on the
-- schema that migration produced.
CREATE INDEX IF NOT EXISTS idx_contacts_ws_email_id ON contacts (workspace_id, lower(email), id);
