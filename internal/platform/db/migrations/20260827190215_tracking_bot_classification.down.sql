-- Reverse of the up migration, restoring the exact prior definitions.
DROP INDEX IF EXISTS idx_tracking_send_recent;

-- Restore idx_tracking_campaign_kind to its migration-000011 definition,
-- character for character: (campaign_id, workspace_id, kind, send_id). Dropping
-- the column below would remove is_machine from the index anyway, but recreating
-- it explicitly means the rolled-back schema equals the pre-migration schema
-- rather than merely resembling it.
DROP INDEX IF EXISTS idx_tracking_campaign_kind;
CREATE INDEX idx_tracking_campaign_kind ON tracking_events (campaign_id, workspace_id, kind, send_id);

-- Dropped explicitly rather than relying on the column drop to cascade it, so
-- this file undoes each statement of the up migration one for one.
ALTER TABLE tracking_events
    DROP CONSTRAINT IF EXISTS tracking_events_machine_reason_matches_verdict;

ALTER TABLE tracking_events
    DROP COLUMN IF EXISTS client_ip,
    DROP COLUMN IF EXISTS machine_reason,
    DROP COLUMN IF EXISTS is_machine;
