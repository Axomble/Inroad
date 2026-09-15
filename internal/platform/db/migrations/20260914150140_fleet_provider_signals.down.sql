-- Symmetric with the up: both tables are new, so dropping them restores the
-- exact prior schema. Their indexes and constraints are owned by the tables and
-- go with them; nothing here existed before this migration, so nothing has to be
-- re-created at a prior definition.
DROP TABLE IF EXISTS fleet_decisions;
DROP TABLE IF EXISTS worker_provider_signals;
