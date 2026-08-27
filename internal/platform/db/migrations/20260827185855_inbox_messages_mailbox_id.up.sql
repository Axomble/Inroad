-- Denormalize mailbox_id onto inbox_messages so CountSentToday's second half
-- becomes sargable.
--
-- This is the escape hatch queries/send.sql's own PERFORMANCE NOTE named, taken
-- because the condition it was waiting for arrived: CountSentToday sits on the
-- SEND PATH — it gates every single send against the mailbox's daily cap — and
-- its inbox_messages half joined via idx_inbox_threads_mailbox (mailbox_id, with
-- NO date component), so the join's outer side was EVERY THREAD THE MAILBOX HAD
-- EVER HAD. That degrades linearly with mailbox age, which means it degrades
-- worst for exactly the long-lived mailboxes that matter most.
--
-- Denormalization is the fix rather than a better index because no index on
-- inbox_threads can help: the DATE lives on inbox_messages and the MAILBOX lives
-- on inbox_threads, and no single index spans two tables. Carrying mailbox_id on
-- the message row puts both columns in one index, so the count range-seeks
-- today's outbound rows directly and scales with REPLY VOLUME instead of total
-- thread history.
--
-- The redundancy is safe because it is immutable: a message never moves between
-- threads (there is no UPDATE of inbox_messages.thread_id anywhere in the repo)
-- and a thread never changes mailbox (inbox_threads.mailbox_id is written once by
-- UpsertInboxThread's INSERT and is not in its DO UPDATE SET list). The value is
-- therefore write-once, and the write is derived from inbox_threads inside the
-- INSERT itself (see queries/inbox.sql) rather than passed in by a caller — a
-- writer cannot forget a column it does not supply.

ALTER TABLE inbox_messages ADD COLUMN mailbox_id UUID REFERENCES mailboxes(id) ON DELETE CASCADE;

-- BACKFILL — LOCKING AND DURATION. Read this before assuming it is online.
--
-- IT IS NOT FULLY ONLINE, and the reason is structural, not a choice made here:
-- golang-migrate's pgx5 driver runs each migration file inside ONE transaction
-- (see internal/platform/db/migrate.go — no x-multi-statement), so no statement
-- in this file can COMMIT. The textbook batched backfill (chunk, COMMIT, repeat)
-- is therefore unavailable without splitting this into an out-of-band job, which
-- would trade a bounded stall for an unbounded window in which the column exists
-- but is not yet populated — and a half-populated denormalized column is exactly
-- the silent under-count this migration exists to prevent.
--
-- What that means concretely:
--   * ADD COLUMN is instant — a nullable column with no default is a catalog-only
--     change in Postgres 11+, no rewrite.
--   * The UPDATE loop takes ROW EXCLUSIVE, which does NOT block concurrent reads
--     (so CountSentToday and the inbox keep serving throughout) and does not block
--     inserts into unrelated rows. It DOES hold one transaction open for the whole
--     backfill, pinning the xmin horizon, so autovacuum reclaims nothing on this
--     table until it finishes.
--   * The CREATE INDEX at the end takes SHARE, which blocks WRITES (the poller's
--     inserts and manual replies) for its duration. Not CONCURRENTLY, because
--     CREATE INDEX CONCURRENTLY cannot run inside a transaction block either.
--
-- Duration at row count: the whole file is a single-table pass plus one partial
-- index build. Order of magnitude on ordinary disk, ~10k rows/chunk at ~15-40ms:
-- ~100k rows ≈ 0.5s, ~1M rows ≈ 2-5s, ~10M rows ≈ 20-60s of write-blocking. For
-- Inroad's actual inbox_messages this is well under a second — the table holds
-- polled replies and hand-sent replies, not campaign sends (those live in
-- `sends`), so it is thousands of rows, not millions. An installation that has
-- somehow grown this table into the tens of millions should run the backfill
-- out-of-band first and re-run this migration, which is safe: the loop selects
-- only rows still NULL, so it is restartable and idempotent, and lands as a no-op
-- if the data is already correct.
--
-- The loop is still chunked rather than one bare UPDATE even inside the single
-- transaction, because chunking bounds each STATEMENT: per-statement memory and
-- the number of row locks any one concurrent writer can queue behind stay flat
-- instead of scaling with the table.
DO $$
DECLARE
    touched BIGINT;
BEGIN
    LOOP
        UPDATE inbox_messages m
        SET mailbox_id = t.mailbox_id
        FROM inbox_threads t
        WHERE m.thread_id = t.id
          AND m.mailbox_id IS NULL
          AND m.id IN (
              SELECT id FROM inbox_messages
              WHERE mailbox_id IS NULL
              ORDER BY id
              LIMIT 10000
          );
        GET DIAGNOSTICS touched = ROW_COUNT;
        EXIT WHEN touched = 0;
    END LOOP;
END $$;

-- NOT NULL, without the table rewrite scan.
--
-- The backfill above is PROVABLY complete: thread_id is NOT NULL and carries a
-- foreign key to inbox_threads(id), so every message row has exactly one thread
-- row, and inbox_threads.mailbox_id is itself NOT NULL. There is no row for which
-- a mailbox_id cannot be derived, and the loop above exits only when no NULL
-- remains. So NOT NULL is correct and is worth having: it is what makes the
-- partial index below total over outbound messages, and it turns "a future writer
-- forgot the column" from a silent under-count (which would silently OVER-SEND
-- past the daily cap — reputation damage, not a test failure) into an immediate
-- constraint violation.
--
-- Done via a validated CHECK rather than a bare SET NOT NULL. A bare SET NOT NULL
-- performs its own full sequential scan while holding ACCESS EXCLUSIVE, which
-- blocks READS as well as writes. Adding a CHECK ... NOT VALID is catalog-only
-- (no scan); VALIDATE CONSTRAINT does the scan under SHARE UPDATE EXCLUSIVE,
-- which blocks neither readers nor writers; Postgres 12+ then recognises the
-- validated CHECK and lets SET NOT NULL skip its scan entirely. So the
-- reads-blocked scan is eliminated. The CHECK is dropped afterwards because the
-- NOT NULL it bootstrapped now carries the same guarantee and the planner reads
-- attnotnull more cheaply than a check expression.
--
-- Caveat consistent with the note above: because the whole file runs in ONE
-- transaction, every lock this section takes is held until the file ends. The
-- benefit here is that no step blocks readers, not that any lock is released
-- early — that is what removes the reads-blocked window, since ACCESS EXCLUSIVE
-- is never taken for a scan.
ALTER TABLE inbox_messages
    ADD CONSTRAINT inbox_messages_mailbox_id_not_null CHECK (mailbox_id IS NOT NULL) NOT VALID;
ALTER TABLE inbox_messages VALIDATE CONSTRAINT inbox_messages_mailbox_id_not_null;
ALTER TABLE inbox_messages ALTER COLUMN mailbox_id SET NOT NULL;
ALTER TABLE inbox_messages DROP CONSTRAINT inbox_messages_mailbox_id_not_null;

-- The index CountSentToday's second half now range-seeks.
--
-- Partial on direction='outbound' because that is the only direction the daily
-- cap counts (an inbound reply consumes none of the mailbox's sending volume),
-- which keeps the index to the small outbound minority of the table. Column order
-- is (mailbox_id, occurred_at): equality first, then the range, so today's
-- half-open occurred_at window is a contiguous index stripe under the mailbox's
-- key rather than a filter applied after reading the mailbox's whole history.
CREATE INDEX idx_inbox_messages_mailbox_outbound
    ON inbox_messages (mailbox_id, occurred_at) WHERE direction = 'outbound';
