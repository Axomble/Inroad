DROP INDEX idx_sends_campaign_sent;

-- Dropping the column drops its CHECK with it.
ALTER TABLE campaigns DROP COLUMN daily_limit;
