-- Indexes first: idx_contacts_search depends on the pg_trgm operator class and
-- on the generated column, so both must outlive it.
DROP INDEX IF EXISTS idx_contacts_ws_email_id;
DROP INDEX IF EXISTS idx_contacts_ws_created;
DROP INDEX IF EXISTS idx_contacts_search;

ALTER TABLE contacts DROP COLUMN IF EXISTS search_text;

-- pg_trgm and btree_gin are intentionally NOT dropped. Extensions are
-- database-wide and cheap to leave installed; dropping one would break any
-- other object that has since come to depend on it, which is a worse failure
-- than a spare extension. Re-running the up migration is a no-op on them
-- (CREATE EXTENSION IF NOT EXISTS).
