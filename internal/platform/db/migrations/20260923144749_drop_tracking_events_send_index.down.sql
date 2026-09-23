-- ONE STATEMENT; see the up file.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_tracking_events_send ON tracking_events (send_id);
