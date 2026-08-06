-- A second, narrower throttle beside daily_limit: daily_limit caps every send
-- (step 1 AND every follow-up) added together, so an operator who only wants to
-- slow down how many BRAND-NEW contacts a campaign starts per day -- while
-- leaving in-flight sequences to keep replying on schedule -- has no way to say
-- that today. NULL means no limit (today's behaviour), exactly like daily_limit.
--
-- Counted against sends.step_order = 1 (see queries/campaign.sql
-- CountFirstStepSendsToday); step_order already lives directly on sends (added by
-- 000007), so no join to sequence_steps is needed to find "the first step".
ALTER TABLE campaigns ADD COLUMN max_new_leads_per_day integer CHECK (max_new_leads_per_day >= 1);

-- The limit is enforced by counting the campaign's step-1 sends for the UTC day
-- on every step-1 advance, so that count must be index-backed. Mirrors 000033's
-- idx_sends_campaign_sent exactly (the daily_limit counterpart): a partial index
-- on just the rows this throttle ever counts, with created_at trailing so the
-- half-open day range range-seeks instead of scanning every send the campaign
-- has ever made. idx_sends_campaign_status (campaign_id, status) does not cover
-- this query at all -- CountFirstStepSendsToday is not filtered by status -- so
-- without this index a long-running campaign that sets max_new_leads_per_day
-- would pay a per-job full-history filter of its own step-1 rows.
CREATE INDEX idx_sends_campaign_step1_created ON sends (campaign_id, created_at) WHERE step_order = 1;
