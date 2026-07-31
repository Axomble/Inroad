DROP TABLE IF EXISTS campaign_senders;

ALTER TABLE sequence_enrollments
    DROP COLUMN IF EXISTS mailbox_id;

ALTER TABLE campaigns
    DROP COLUMN IF EXISTS rotation_mode;

-- Restore the stop-reason CHECK exactly as 000014 left it. A row already stopped
-- with 'mailbox_removed' would fail the re-add, so those are rewritten to the
-- closest surviving reason ('failed' — the engine could not proceed) rather than
-- leaving the down migration unable to run.
UPDATE sequence_enrollments SET stop_reason = 'failed' WHERE stop_reason = 'mailbox_removed';
ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_stop_reason_check;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_stop_reason_check
    CHECK (stop_reason IS NULL OR stop_reason IN
        ('replied','bounced','suppressed','manual','failed','unsubscribed'));
