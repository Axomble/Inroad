-- Claim-before-send: make delivery idempotent. The sends row IS the delivery
-- claim, moving through queued/sending -> sent|failed. A retried asynq job or a
-- concurrent sweeper-vs-lazy-chain advance can then no-op instead of delivering
-- the same email twice.
--
-- 'sending' is the transient claim state; claimed_at is the lease timestamp a
-- crashed worker's row is reclaimed by once the lease expires.

-- The status CHECK is the inline (auto-named) constraint from 000003.
ALTER TABLE sends DROP CONSTRAINT sends_status_check;
ALTER TABLE sends ADD CONSTRAINT sends_status_check
    CHECK (status IN ('queued','sending','sent','failed','skipped'));

ALTER TABLE sends ADD COLUMN claimed_at TIMESTAMPTZ;
