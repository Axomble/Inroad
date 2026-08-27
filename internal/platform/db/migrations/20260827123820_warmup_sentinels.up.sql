-- Sentinels: mailboxes the operator controls end to end and is willing to expose to
-- any lane, so a degrading mailbox has something dependable to be measured against.
--
-- A FLAG, not a lane, and that is the load-bearing choice. The design's table lists
-- `sentinel` in the lane column, but a sentinel has its own health and its own lane
-- like every participant — it can degrade, be contained, and recover — and folding
-- that into `lane` would make "sentinel" and "watch" mutually exclusive when a
-- sentinel that starts degrading is exactly the case that must stay representable.
-- One column with two meanings is the defect this subsystem has been corrected for
-- more than once (`inbox` that also meant "primary", `bounce_rate` that also counted
-- soft bounces).
--
-- Default false, so every existing pool keeps behaving exactly as it did. That is not
-- a migration convenience: most self-hosted installations will never designate a
-- sentinel, and design §4 requires the no-sentinel case to keep working rather than
-- degrade.
ALTER TABLE warmup_participants
    ADD COLUMN IF NOT EXISTS is_sentinel BOOLEAN NOT NULL DEFAULT false;

-- Partner selection filters on it for every candidate, alongside enabled and lane.
CREATE INDEX IF NOT EXISTS idx_warmup_participants_sentinel
    ON warmup_participants (workspace_id, is_sentinel)
    WHERE is_sentinel;
