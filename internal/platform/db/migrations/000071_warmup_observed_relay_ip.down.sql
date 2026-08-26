-- Re-runnable: a partially rolled-back schema must not block a retry.
ALTER TABLE warmup_observations
    DROP COLUMN IF EXISTS observed_relay_ip;
