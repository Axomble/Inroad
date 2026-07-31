DROP TABLE IF EXISTS campaign_senders;

ALTER TABLE sequence_enrollments
    DROP COLUMN IF EXISTS mailbox_id;

ALTER TABLE campaigns
    DROP COLUMN IF EXISTS rotation_mode;
