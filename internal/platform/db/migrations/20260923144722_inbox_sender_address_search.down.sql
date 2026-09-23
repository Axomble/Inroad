-- pg_trgm and btree_gin stay installed: they predate this migration (000034)
-- and other indexes depend on them.
DROP INDEX IF EXISTS idx_inbox_messages_from_email_search;
