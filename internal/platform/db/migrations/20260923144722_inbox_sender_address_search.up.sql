-- Inbox search by sender address: GET /inbox/search matches a query against
-- the From address of a thread's inbound messages (as well as the linked
-- contact's email, which idx_contacts_search from 000034 already serves).
--
-- The contact's email covers a campaign thread; the From address is the only
-- address a legacy thread with no linked contact has, and the one a reply from
-- a colleague of the contact carries. It is a substring match (an operator
-- types "acme.com" or "jo@"), so the index is trigram, not btree — the same
-- shape as idx_contacts_search: workspace_id leads (btree_gin, 000034) so a
-- search never reads another tenant's entries, and lower() matches the query's
-- case-insensitive predicate exactly, which an expression index requires.
--
-- Partial on direction = 'inbound' because only inbound From addresses are
-- searched: an outbound message's From is one of the workspace's own mailboxes,
-- which would make every thread match its own mailbox address.
--
-- Not CONCURRENTLY, for the transaction-per-file reason given in
-- 20260923105701_inbox_full_text_search; the build holds SHARE (blocks the
-- poller's inserts, not reads) for the sub-second a thousands-row table needs.
-- IF NOT EXISTS lets a very large installation pre-build it CONCURRENTLY by hand.
CREATE INDEX IF NOT EXISTS idx_inbox_messages_from_email_search
    ON inbox_messages USING gin (workspace_id, lower(from_email) gin_trgm_ops)
    WHERE direction = 'inbound';
