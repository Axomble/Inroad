-- The queries at the prior version derive opens_measurable from the campaign's
-- current tracking_enabled again. The per-send values stamped since the up
-- migration are lost: re-applying it re-derives them from the backfill signals,
-- not from what the claims stamped.
ALTER TABLE sends DROP COLUMN IF EXISTS tracked;
