-- Reverse of the up file.
--
-- Dropping tracking_event_rollups DESTROYS every rolled-up engagement count: a
-- campaign's historical open/click numbers drop to whatever raw tracking_events
-- rows are still inside the window. Nothing can recompute them, because the raw
-- rows they came from were deleted when they were rolled up. Run this only on a
-- deployment whose retention sweep has never rolled anything up, or accept that
-- loss knowingly.
DROP TABLE IF EXISTS retention_cursors;
DROP VIEW IF EXISTS tracking_engagement;
DROP TABLE IF EXISTS tracking_event_rollups;
