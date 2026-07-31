-- Stacked sending limits: a campaign-wide ceiling on top of the per-mailbox
-- daily_cap. Until now the only ceiling was per-mailbox, so "this campaign sends
-- at most 100/day" meant editing every mailbox in its pool — which throttles
-- those mailboxes' OTHER campaigns too.
--
-- NULL means no campaign limit (today's behaviour). The value is a campaign-wide
-- total per UTC day across the whole sender pool, not a per-mailbox figure: that
-- is what an operator means by it. It can only ever LOWER throughput — no
-- campaign setting raises a mailbox above its own ramped, health-scaled cap.
ALTER TABLE campaigns ADD COLUMN daily_limit INT
    CHECK (daily_limit IS NULL OR daily_limit > 0);

-- The limit is enforced by counting the campaign's sends for the UTC day on
-- every advance, so that count must be index-backed. Mirrors
-- idx_sends_mailbox_sent exactly (the mailbox-cap counterpart): a partial index
-- on the sent rows only, with sent_at trailing so the half-open day range
-- range-seeks instead of scanning every send the campaign has ever made.
-- idx_sends_campaign_status (campaign_id, status) would seek to the campaign's
-- sent rows and then filter all of them, which grows with the campaign's whole
-- history rather than with today.
CREATE INDEX idx_sends_campaign_sent ON sends (campaign_id, sent_at) WHERE status = 'sent';
