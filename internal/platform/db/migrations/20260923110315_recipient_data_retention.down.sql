-- Reverse of the up file, in reverse order.
--
-- Dropping tracking_event_rollups DESTROYS every rolled-up engagement count: a
-- campaign's historical open/click numbers drop to whatever raw tracking_events
-- rows are still inside the window. Nothing can recompute them, because the raw
-- rows they came from were deleted when they were rolled up. Run this only on a
-- deployment whose retention sweep has never rolled anything up, or accept that
-- loss knowingly.
DROP INDEX IF EXISTS idx_inbox_threads_campaign_contact;
DROP INDEX IF EXISTS idx_sends_created_at;
DROP INDEX IF EXISTS idx_inbox_threads_last_message_at;
DROP INDEX IF EXISTS idx_deliverability_events_received_at;
DROP INDEX IF EXISTS idx_tracking_events_created_at;
DROP VIEW IF EXISTS tracking_engagement;
DROP TABLE IF EXISTS tracking_event_rollups;
