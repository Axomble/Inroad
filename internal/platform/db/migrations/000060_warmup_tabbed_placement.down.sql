-- Reverse of 000060.
--
-- `tabbed` rows have to be resolved BEFORE the narrow CHECK returns, because the
-- old vocabulary has no value for them — this is the whole reason a rollback of
-- this migration is not a formality.
--
-- They become 'inbox', which is what they said before 000060 existed: a Promotions
-- landing was recorded as an inbox landing, it incremented the inbox counter in
-- warmup_daily_stats, and it counted as one placement sample. Rolling back
-- therefore restores exactly the pre-000060 state — the TAB is lost, the
-- placement, its daily projection and its sample are not.
--
-- Deleting the rows is the obvious alternative and it is wrong twice over: it
-- would destroy reputation evidence in order to undo a vocabulary change, and it
-- would leave the daily projection those rows already fed disagreeing with the
-- observations table about the same messages.
UPDATE warmup_receipts SET placement = 'inbox' WHERE placement = 'tabbed';
UPDATE warmup_observations SET placement = 'inbox' WHERE placement = 'tabbed';

-- Dropping the columns drops the CHECKs that depend on them, including the
-- cross-column tabbed_within_capable.
ALTER TABLE warmup_signal_snapshots
    DROP COLUMN IF EXISTS placement_tabbed,
    DROP COLUMN IF EXISTS placement_tab_capable;

-- Dropping tab_capable takes warmup_observations_tabbed_requires_capability with
-- it, which is why the placement rewrite above has to come first: the constraint
-- forbids exactly the rows that rewrite removes.
ALTER TABLE warmup_observations DROP COLUMN IF EXISTS tab_capable;

-- Re-add the EXACT prior definitions (migration 000018 for receipts, 000054 for
-- observations), so a re-applied 000060 finds the constraints it expects to drop.
ALTER TABLE warmup_observations DROP CONSTRAINT warmup_observations_placement_check;
ALTER TABLE warmup_observations ADD CONSTRAINT warmup_observations_placement_check
    CHECK (placement IN ('inbox','spam','other'));

ALTER TABLE warmup_receipts DROP CONSTRAINT warmup_receipts_placement_check;
ALTER TABLE warmup_receipts ADD CONSTRAINT warmup_receipts_placement_check
    CHECK (placement IN ('inbox','spam','other'));
