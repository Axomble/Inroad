-- The queries at the prior version derive opens_measurable from the campaign's
-- current tracking_enabled again. The per-send values stamped since the up
-- migration are lost: re-applying the two migrations re-derives them from the
-- backfill's signals, not from what the claims stamped.
ALTER TABLE sends DROP COLUMN IF EXISTS tracked;
