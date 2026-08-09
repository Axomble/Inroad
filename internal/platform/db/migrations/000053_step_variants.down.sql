-- Dropping sends.variant_id discards attribution for every message already sent
-- under a variant. That is unavoidable: the column IS the attribution, and there
-- is nowhere else to keep it.
ALTER TABLE sends DROP CONSTRAINT IF EXISTS sends_variant_workspace_fkey;
DROP INDEX IF EXISTS idx_sends_variant;
ALTER TABLE sends DROP COLUMN IF EXISTS variant_id;

DROP TABLE IF EXISTS sequence_step_variants;

ALTER TABLE sequence_steps DROP COLUMN IF EXISTS variant_weight;
