-- Revert the stop-reason CHECK to its 000008 shape (without 'unsubscribed').
ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_stop_reason_check;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_stop_reason_check
    CHECK (stop_reason IS NULL OR stop_reason IN
        ('replied','bounced','suppressed','manual','failed'));

ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_reply_class_chk;
ALTER TABLE sequence_enrollments
    DROP COLUMN replied_at,
    DROP COLUMN reply_confidence,
    DROP COLUMN reply_source,
    DROP COLUMN reply_class;
