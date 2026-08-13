-- Reverse of 000058: drop the constraint before the column it references.
--
-- The label is lost, not the figures — bounce_samples/bounce_rate keep whatever
-- the driving arm wrote. Rolling back therefore returns the trail to exactly the
-- pre-000058 state: a bounce pair whose population is unrecorded.
ALTER TABLE warmup_state_transitions
    DROP CONSTRAINT IF EXISTS warmup_state_transitions_bounce_population_check;

ALTER TABLE warmup_state_transitions
    DROP COLUMN IF EXISTS bounce_population;
