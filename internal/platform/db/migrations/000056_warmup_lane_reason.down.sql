-- The lane's explanation survives on warmup_state_transitions either way, so
-- dropping the denormalised copy loses no history — only the overview's ability to
-- show it without a per-row join.
ALTER TABLE warmup_participants
    DROP COLUMN IF EXISTS lane_reason;
