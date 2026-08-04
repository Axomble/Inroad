DROP INDEX idx_sends_campaign_step1_created;

-- Dropping the column drops its CHECK with it.
ALTER TABLE campaigns DROP COLUMN max_new_leads_per_day;
