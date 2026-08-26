DROP INDEX IF EXISTS idx_inbox_messages_thread_inbound;
DROP FUNCTION IF EXISTS inbox_thread_awaiting_reply(UUID, UUID, UUID, UUID);
