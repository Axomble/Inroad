ALTER TABLE campaigns DROP COLUMN IF EXISTS tracking_enabled;
DROP TABLE IF EXISTS tracking_events;
DROP TYPE IF EXISTS tracking_event_kind;
