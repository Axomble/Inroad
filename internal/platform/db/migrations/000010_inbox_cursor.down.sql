DROP INDEX IF EXISTS idx_sends_message_id;
ALTER TABLE mailboxes DROP COLUMN IF EXISTS inbox_uid_validity;
ALTER TABLE mailboxes DROP COLUMN IF EXISTS inbox_last_seen_uid;
