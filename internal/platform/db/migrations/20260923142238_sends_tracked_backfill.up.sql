-- Backfill sends.tracked (added, nullable, by 20260923105515) for every row
-- that predates it. The per-send truth was never stored, so it cannot be
-- recovered exactly. One pass decides each row from, strongest first:
--
--   1. A tracking event exists for the send (open or click, human OR machine —
--      a scanner fetching the pixel still proves the pixel was in the message).
--      That is proof the send was tracked, whatever the campaign says today.
--   2. Otherwise, the campaign's tracking_enabled as of this migration: the
--      signal the old query read, so no row without proof changes its answer —
--      it is frozen, not improved.
--
-- What CANNOT be recovered, and stays exactly as wrong as it already was:
--   * a send made while tracking was ON, on a campaign whose tracking is now
--     OFF, that never produced a single event -> false (it was tracked; nobody
--     opened it, and nothing records that the pixel was there);
--   * a send made while tracking was OFF, on a campaign whose tracking is now ON
--     -> true (it was not tracked; no event can exist to correct it);
--   * a text-only step (no HTML body, so no pixel) on a tracked campaign ->
--     true. The step's body at send time was not recorded, and today's step
--     content is as mutable as today's toggle, so it is not used.
-- The toggle's history is logged nowhere, so no better signal exists.
--
-- BATCHED, and why this file must stay ONE statement. golang-migrate sends a
-- migration file to Postgres as a single simple-protocol query. A file of several
-- statements runs as one implicit transaction, so one big UPDATE would hold row
-- locks on all of sends (blocking every claim and finalize touching them) until
-- the last row. A lone DO block is NOT inside a transaction block, which is what
-- lets it COMMIT between batches: each batch of at most 5,000 rows, walked in
-- primary-key order, holds its row locks only until its own COMMIT. Adding a
-- second statement to this file would turn every COMMIT below into "invalid
-- transaction termination".
--
-- Duration scales linearly with the table: one PK range scan plus, per row, one
-- probe of idx_tracking_events_send and one campaigns PK lookup. It was not
-- benchmarked at production scale; progress is visible as the count of
-- `tracked IS NULL` rows falling.
--
-- RE-RUNNABLE and self-limiting: it only touches `tracked IS NULL`, so a run
-- interrupted part-way (the schema is left dirty; force the previous version and
-- re-run) resumes rather than redoing work, and it never overwrites a value a
-- claim stamped. Rows an old-binary worker inserts AFTER this runs, during a
-- rolling deploy, stay NULL and read the campaign's flag through the readers'
-- COALESCE — the old behaviour, for the few minutes' worth of sends the rollout
-- window produces.
DO $$
DECLARE
    last_id  uuid := '00000000-0000-0000-0000-000000000000';
    batch_to uuid;
BEGIN
    LOOP
        -- The batch's upper bound: the 5,000th id after the last one (uuid has
        -- no max() aggregate, so the bound is read off the ordered slice).
        SELECT b.id INTO batch_to
          FROM (SELECT id FROM sends WHERE id > last_id ORDER BY id LIMIT 5000) b
         ORDER BY b.id DESC
         LIMIT 1;
        EXIT WHEN batch_to IS NULL;

        UPDATE sends s
           SET tracked =
                   EXISTS (SELECT 1 FROM tracking_events te
                            WHERE te.send_id = s.id AND te.workspace_id = s.workspace_id)
                OR EXISTS (SELECT 1 FROM campaigns c
                            WHERE c.id = s.campaign_id AND c.workspace_id = s.workspace_id
                              AND c.tracking_enabled)
         WHERE s.id > last_id AND s.id <= batch_to
           AND s.tracked IS NULL;

        last_id := batch_to;
        COMMIT;
    END LOOP;
END
$$;
