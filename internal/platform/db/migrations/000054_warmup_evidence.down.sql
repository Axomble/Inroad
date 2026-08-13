-- Unknown cannot satisfy the old CHECK constraint; map it to the old neutral
-- state before restoring the v1 vocabulary.
UPDATE warmup_participants
SET health_state = 'healthy',
    health_reason = ''
WHERE health_state = 'unknown';

DROP TABLE IF EXISTS warmup_state_transitions;
DROP TABLE IF EXISTS warmup_observations;

ALTER TABLE warmup_participants
    DROP CONSTRAINT warmup_participants_health_state_check;
ALTER TABLE warmup_participants
    ALTER COLUMN health_state SET DEFAULT 'healthy';
ALTER TABLE warmup_participants
    ADD CONSTRAINT warmup_participants_health_state_check
    CHECK (health_state IN ('healthy','watch','throttled','paused'));
