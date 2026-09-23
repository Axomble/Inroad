-- Whether a send carried tracking is a fact about THAT message, fixed the moment
-- it was built. Until now nothing recorded it: ContactSendStats derived
-- opens_measurable from campaigns.tracking_enabled, which is the campaign's
-- CURRENT setting. Turning tracking off therefore retroactively declared every
-- past tracked send unmeasurable (and turning it on declared never-tracked sends
-- measurable) — a toggle rewrote a contact's history.
--
-- From this migration on, ClaimStepSend stamps sends.tracked from the job being
-- claimed (coreapi.StepSendJob.CarriesTracking: the campaign had tracking on AND
-- the step had an HTML body, since the pixel and the link rewriting only exist
-- in HTML), and the aggregate reads the row.
--
-- DEFAULT false: a writer that does not state it must not claim that opens were
-- measurable. Adding a column with a constant default is metadata-only.
ALTER TABLE sends ADD COLUMN tracked BOOLEAN NOT NULL DEFAULT false;

-- BACKFILL. The per-send truth was never stored, so it cannot be recovered
-- exactly for existing rows. The signals used, strongest first:
--
--   1. A tracking event exists for the send (open or click, human OR machine —
--      a scanner fetching the pixel still proves the pixel was in the message).
--      That is proof the send was tracked, whatever the campaign says today.
--   2. Otherwise, the campaign's tracking_enabled as of this migration. This is
--      the exact signal the old query used, so for every row without proof the
--      answer rendered today is preserved: frozen, not improved.
--
-- What CANNOT be recovered, and stays exactly as wrong as it already was:
--   * a send made while tracking was ON, on a campaign whose tracking is now
--     OFF, that never produced a single event -> backfilled false (it was
--     tracked; nobody opened it, and nothing records that the pixel was there);
--   * a send made while tracking was OFF, on a campaign whose tracking is now ON
--     -> backfilled true (it was not tracked; no event can exist to correct it);
--   * a text-only step (no HTML body, so no pixel) on a tracked campaign ->
--     backfilled true. The step's body at send time was not recorded, and
--     today's step content is as mutable as today's toggle, so it is not used.
-- The toggle's history is logged nowhere, so no better signal exists. New sends
-- are stamped correctly at claim time; only these historical rows are estimates.
UPDATE sends s
   SET tracked = true
  FROM campaigns c
 WHERE c.id = s.campaign_id
   AND c.workspace_id = s.workspace_id
   AND c.tracking_enabled;

UPDATE sends s
   SET tracked = true
 WHERE NOT s.tracked
   AND EXISTS (
       SELECT 1 FROM tracking_events te
        WHERE te.send_id = s.id AND te.workspace_id = s.workspace_id
   );
