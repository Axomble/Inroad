-- Back to the server defaults.
ALTER TABLE deliverability_events RESET (autovacuum_vacuum_scale_factor, autovacuum_vacuum_threshold);
ALTER TABLE tracking_events RESET (autovacuum_vacuum_scale_factor, autovacuum_vacuum_threshold);
ALTER TABLE sends RESET (autovacuum_vacuum_scale_factor, autovacuum_vacuum_threshold);
