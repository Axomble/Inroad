-- Gmail (and future API providers) track inbox position by an opaque, monotonic
-- historyId string, not the IMAP UID/UIDVALIDITY pair. Store it separately so
-- the existing IMAP cursor columns (inbox_last_seen_uid/inbox_uid_validity) are
-- untouched and the reply/bounce path keeps working unchanged.
ALTER TABLE mailboxes ADD COLUMN inbox_cursor TEXT NOT NULL DEFAULT '';
