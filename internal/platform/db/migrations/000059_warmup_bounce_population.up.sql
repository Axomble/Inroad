-- RENUMBERED from 000058 to sit above the renumbered pair-leases migration.
--
-- Name which bounce population a transition's bounce_samples/bounce_rate describe.
--
-- The engine keeps campaign and warmup hard bounces apart deliberately (000055):
-- pooling them let synthetic warmup traffic dilute a real campaign bounce rate
-- below its own threshold. The table has ONE bounce column pair, and the writer
-- records whichever arm actually DROVE the decision with its own denominator — so
-- without this column a row can read "campaign hard bounces crossed the pause
-- threshold" beside a figure labelled only "hard bounces", or report a
-- warmup-driven pause next to a campaign denominator of zero.
--
-- NULLABLE, and deliberately not backfilled: rows written before the split
-- genuinely do not know which arm spoke, and inferring one from reason_code would
-- put a claim in an append-only audit trail that nobody measured. Same reasoning
-- as 000055's lane columns. New rows always set it — enforced by the writer, which
-- takes the population from the same call that picks the pair
-- (warmup.Decision.DrivingBouncePair).
ALTER TABLE warmup_state_transitions
    ADD COLUMN IF NOT EXISTS bounce_population TEXT;

ALTER TABLE warmup_state_transitions
    ADD CONSTRAINT warmup_state_transitions_bounce_population_check
    CHECK (bounce_population IS NULL OR bounce_population IN ('campaign','warmup'));
