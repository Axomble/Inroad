-- IMAP poll cursor per mailbox, for reply/bounce detection. last_seen_uid is
-- the highest UID the poller has processed; uid_validity guards against a
-- server-side UIDVALIDITY change (RFC 3501), which invalidates the cursor and
-- forces a resync from UID 1.
ALTER TABLE mailboxes
    ADD COLUMN inbox_last_seen_uid BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN inbox_uid_validity  BIGINT NOT NULL DEFAULT 0;

-- GetSendByMessageID runs on every inbound message the poller sees (~200 per
-- mailbox per 3-minute poll), matching sends.message_id against the inbound
-- Message-ID/In-Reply-To/References headers. Partial: most sends rows have
-- message_id = '' (queued/failed/skipped), so the index only indexes the ones
-- that could ever match.
CREATE INDEX idx_sends_message_id ON sends (workspace_id, message_id) WHERE message_id <> '';
