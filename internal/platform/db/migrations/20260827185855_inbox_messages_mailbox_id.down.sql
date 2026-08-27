-- Reverses the up migration exactly. Dropping the column drops the partial index
-- and the FK constraint with it; the explicit DROP INDEX is stated first anyway so
-- the intent is readable and so a partially-applied up (index created, column drop
-- attempted separately by a future edit) still reverses cleanly.
--
-- Nothing needs re-adding on the way down: idx_inbox_threads_mailbox (migration
-- 000049) was never dropped by the up — CountSentToday's pre-denormalization form
-- still has the index it needs the moment queries/send.sql reverts with this.
DROP INDEX IF EXISTS idx_inbox_messages_mailbox_outbound;
ALTER TABLE inbox_messages DROP COLUMN mailbox_id;
