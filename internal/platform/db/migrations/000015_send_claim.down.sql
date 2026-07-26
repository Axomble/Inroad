-- Reverse 000015. Any row left mid-claim ('sending') cannot satisfy the
-- reverted CHECK, so fail it forward first (a downgrade-time operational
-- concern per the design spec §2.1) before re-adding the exact prior CHECK.
UPDATE sends SET status = 'failed' WHERE status = 'sending';

ALTER TABLE sends DROP CONSTRAINT sends_status_check;
ALTER TABLE sends ADD CONSTRAINT sends_status_check
    CHECK (status IN ('queued','sent','failed','skipped'));

ALTER TABLE sends DROP COLUMN claimed_at;
