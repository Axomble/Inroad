DROP INDEX IF EXISTS idx_inbox_threads_campaign_contact;

ALTER TABLE sequence_enrollments DROP COLUMN awaiting_condition_step;

DROP TABLE sequence_step_branches;

ALTER TABLE sequence_steps DROP CONSTRAINT sequence_steps_id_campaign_key;
