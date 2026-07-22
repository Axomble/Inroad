-- Fix 3 (idempotency backstop): at most one send row per (campaign, contact,
-- step). A duplicate sequence:advance (sweeper re-enqueue racing the lazy chain)
-- can then no-op via RecordStepSend's ON CONFLICT instead of writing a second
-- row / implying a second email. Partial predicate documents intent; step_order
-- is currently NOT NULL DEFAULT 1, so the index also covers direct sends — that
-- is harmless (they are one row per (campaign, contact)) and in fact restores
-- the per-contact idempotency 000006 relaxed for partition-readiness.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sends_campaign_contact_step
    ON sends (campaign_id, contact_id, step_order)
    WHERE step_order IS NOT NULL;

-- Fix 2 (bounded cap-defer loop): per-enrollment counter mirroring sends.attempts.
-- The advance handler bumps it each time it defers an over-cap step and stops the
-- enrollment 'failed' once it exceeds the ceiling.
ALTER TABLE sequence_enrollments ADD COLUMN cap_deferrals INT NOT NULL DEFAULT 0;

-- Fix 2: allow 'failed' as a stop reason (degenerate cap of 0, or cap-defer
-- ceiling exhausted). The single stop entry point records it like any other halt.
ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_stop_reason_check;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_stop_reason_check
    CHECK (stop_reason IS NULL OR stop_reason IN ('replied','bounced','suppressed','manual','failed'));
