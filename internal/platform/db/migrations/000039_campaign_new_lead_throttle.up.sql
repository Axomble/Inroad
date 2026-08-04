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
