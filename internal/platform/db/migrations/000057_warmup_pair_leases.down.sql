-- Reverse of 000057. Rolling back gives up lease revalidation and the symmetric
-- pair budget; the send path returns to the directional cap it had before, which
-- is the pre-lease behaviour and not a silent promotion of anything.
--
-- The index is dropped explicitly before its column even though dropping pair_key
-- would take it with it, so the reversal reads in the same order it was written.
DROP INDEX IF EXISTS idx_warmup_sends_pair_day;

ALTER TABLE warmup_sends
    DROP COLUMN IF EXISTS pair_key;

ALTER TABLE warmup_sends
    DROP COLUMN IF EXISTS issued_constraints,
    DROP COLUMN IF EXISTS lease_expires_at,
    DROP COLUMN IF EXISTS issued_policy_version,
    DROP COLUMN IF EXISTS issued_lane;
