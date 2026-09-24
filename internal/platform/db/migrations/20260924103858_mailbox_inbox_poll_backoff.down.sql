-- Dropping the columns restores the previous behaviour exactly: the poll
-- fan-out's predicate goes back to `status = 'active'` alone, so every active
-- mailbox becomes eligible on the next sweep. Nothing else reads them.
ALTER TABLE mailboxes
    DROP COLUMN IF EXISTS inbox_poll_failures,
    DROP COLUMN IF EXISTS inbox_poll_retry_after;
