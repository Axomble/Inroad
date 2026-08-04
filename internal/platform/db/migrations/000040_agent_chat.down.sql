-- Reverse of 000040_agent_chat.up.sql. Dropped child-first so the composite
-- tenant foreign keys unwind cleanly; each table's indexes go with it.
DROP TABLE IF EXISTS agent_runs;
DROP TABLE IF EXISTS agent_message_parts;
DROP TABLE IF EXISTS agent_messages;
DROP TABLE IF EXISTS agent_threads;
