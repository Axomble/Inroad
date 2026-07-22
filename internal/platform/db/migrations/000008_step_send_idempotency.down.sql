ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_stop_reason_check;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_stop_reason_check
    CHECK (stop_reason IS NULL OR stop_reason IN ('replied','bounced','suppressed','manual'));
ALTER TABLE sequence_enrollments DROP COLUMN IF EXISTS cap_deferrals;
DROP INDEX IF EXISTS idx_sends_campaign_contact_step;
