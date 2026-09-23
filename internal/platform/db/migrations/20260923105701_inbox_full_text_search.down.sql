-- Indexes first: each depends on inbox_search_document, so the function must
-- outlive them. btree_gin stays installed — it predates this migration (000034)
-- and other indexes depend on it.
DROP INDEX IF EXISTS idx_sequence_step_variants_search;
DROP INDEX IF EXISTS idx_sequence_steps_search;
DROP INDEX IF EXISTS idx_inbox_messages_search;

DROP FUNCTION IF EXISTS inbox_search_query(TEXT);
DROP FUNCTION IF EXISTS inbox_search_document(TEXT, TEXT);
