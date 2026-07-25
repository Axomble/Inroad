-- Record the classified inbound reply on the enrollment: reply_class is the
-- sentiment/intent bucket, reply_source names the layer that decided it
-- (header/lexicon/model), reply_confidence is that layer's score, and
-- replied_at stamps when the reply was classified. All nullable — set only
-- once a matched reply is processed; a never-replied enrollment leaves them
-- NULL. The CHECK pins reply_class to the known classes (or NULL) so a bad
-- writer can't smuggle in an unknown bucket.
ALTER TABLE sequence_enrollments
    ADD COLUMN reply_class text,
    ADD COLUMN reply_source text,
    ADD COLUMN reply_confidence real,
    ADD COLUMN replied_at timestamptz;

ALTER TABLE sequence_enrollments
    ADD CONSTRAINT sequence_enrollments_reply_class_chk
    CHECK (reply_class IS NULL OR reply_class IN
        ('positive','negative','neutral','auto_reply','out_of_office','unsubscribe','unknown'));

-- Extend the stop-reason CHECK (last set in 000008) with 'unsubscribed' so a
-- reply that opts out can stop the enrollment with a compliance-specific
-- reason distinct from a plain 'replied' stop.
ALTER TABLE sequence_enrollments DROP CONSTRAINT sequence_enrollments_stop_reason_check;
ALTER TABLE sequence_enrollments ADD CONSTRAINT sequence_enrollments_stop_reason_check
    CHECK (stop_reason IS NULL OR stop_reason IN
        ('replied','bounced','suppressed','manual','failed','unsubscribed'));
