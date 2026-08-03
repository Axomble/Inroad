DROP INDEX IF EXISTS idx_enrollments_campaign_bounced;

-- Dropping the three columns drops their CHECK constraints with them.
ALTER TABLE campaigns
    DROP COLUMN IF EXISTS complaint_pause_pct,
    DROP COLUMN IF EXISTS bounce_pause_pct,
    DROP COLUMN IF EXISTS auto_pause_enabled;

-- Restore the EXACT pre-000037 reason vocabulary. Existing 'complaint' rows would
-- fail that CHECK, so they are folded into 'unsubscribe' first: a complaint IS an
-- opt-out, so the suppression itself survives the rollback (never re-mailing the
-- address is the load-bearing part) and only the distinction between the two kinds
-- of opt-out is lost.
UPDATE suppression SET reason = 'unsubscribe' WHERE reason = 'complaint';
ALTER TABLE suppression DROP CONSTRAINT suppression_reason_check;
ALTER TABLE suppression ADD CONSTRAINT suppression_reason_check
    CHECK (reason IN ('unsubscribe','bounce','manual'));

-- Dropping the tables drops their indexes and FKs with them. The ingested events
-- are lost, which is correct: without the endpoint there is nothing to read them.
DROP TABLE IF EXISTS deliverability_events;
DROP TABLE IF EXISTS campaign_pause_events;
