-- Reverse of 000055. Order matters: drop constraints before the columns they
-- reference, and drop the snapshot table before the mailboxes it points at are
-- relied upon by anything else.

DROP INDEX IF EXISTS idx_sequence_enrollments_bounced;
DROP INDEX IF EXISTS idx_warmup_sends_message_id;

ALTER TABLE warmup_observations
    DROP CONSTRAINT IF EXISTS warmup_observations_invalid_token_unattributed;
ALTER TABLE warmup_observations
    DROP CONSTRAINT IF EXISTS warmup_observations_invalid_token_untrusted;

ALTER TABLE warmup_state_transitions
    DROP CONSTRAINT IF EXISTS warmup_state_transitions_to_lane_check;
ALTER TABLE warmup_state_transitions
    DROP CONSTRAINT IF EXISTS warmup_state_transitions_from_lane_check;
ALTER TABLE warmup_state_transitions
    DROP COLUMN IF EXISTS lane_reason,
    DROP COLUMN IF EXISTS lane_reason_code,
    DROP COLUMN IF EXISTS to_lane,
    DROP COLUMN IF EXISTS from_lane;

DROP TABLE IF EXISTS warmup_signal_snapshots;

ALTER TABLE deliverability_events
    DROP CONSTRAINT IF EXISTS deliverability_events_bounce_class_check;
ALTER TABLE deliverability_events
    DROP COLUMN IF EXISTS bounce_class;

-- Dropping lane discards the pool-eligibility axis entirely. health_state is left
-- exactly as Phase 1 last computed it, which is correct: health was never derived
-- from lane, so there is nothing to recompute and nothing to restore. Unlike
-- 000054's rollback, this one cannot silently promote anyone — a mailbox that was
-- quarantined by lane while healthy by reputation returns to being gated only by
-- health, which is precisely the pre-Phase-1 behaviour.
ALTER TABLE warmup_participants
    DROP CONSTRAINT IF EXISTS warmup_participants_lane_check;
ALTER TABLE warmup_participants
    DROP COLUMN IF EXISTS lane;
