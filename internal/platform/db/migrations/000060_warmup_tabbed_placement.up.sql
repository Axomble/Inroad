-- Provider-native tabbed placement, Phase 2 slice A. See
-- docs/superpowers/specs/2026-08-14-warmup-provider-native-placement-design.md.
--
-- Gmail's Promotions tab IS the inbox as far as this engine has been counting, so
-- a mailbox whose warmup mail reliably lands there reports a 100% inbox rate and
-- is promoted on the strength of mail almost nobody opens — the same defect family
-- as soft bounces counted as hard: a number whose name does not match what it
-- measures.
--
-- `tabbed` is recorded ONLY when a provider positively identifies a tab. `inbox`
-- deliberately keeps its meaning ("landed in the inbox"), because redefining it to
-- mean "primary" would give one column two meanings — "primary inbox" on Gmail and
-- "inbox, tab unknowable" on IMAP — differing by a provider the reader does not
-- record.

-- BOTH CHECKs widen, and they have to agree. The receipt is what the poller writes
-- and the observation is what the policy reads, in ONE transaction, so a value one
-- accepts and the other rejects aborts the whole receipt rather than degrading.
ALTER TABLE warmup_receipts DROP CONSTRAINT warmup_receipts_placement_check;
ALTER TABLE warmup_receipts ADD CONSTRAINT warmup_receipts_placement_check
    CHECK (placement IN ('inbox','tabbed','spam','other'));

ALTER TABLE warmup_observations DROP CONSTRAINT warmup_observations_placement_check;
ALTER TABLE warmup_observations ADD CONSTRAINT warmup_observations_placement_check
    CHECK (placement IS NULL OR placement IN ('inbox','tabbed','spam','other'));

-- Whether the reader that produced THIS observation could have seen a category
-- label at all.
--
-- It is a property of the OBSERVING PATH, not of the mailbox. Deriving it from
-- mailboxes.provider at read time would be wrong the moment a mailbox is migrated
-- between providers: historical observations would retroactively claim a
-- capability the reader that wrote them never had. Recording it per row makes the
-- claim immutable and true of the row.
--
-- Existing rows keep false, which is accurate of them — they were written by a
-- reader that fetched Gmail's labels and discarded them one line before they would
-- have been useful.
ALTER TABLE warmup_observations
    ADD COLUMN tab_capable BOOLEAN NOT NULL DEFAULT false;

-- Materialized per participant beside placement_inbox/placement_spam, for the
-- reason those exist: the sweep computes evidence once per workspace rather than
-- re-deriving it per participant on every tick.
--
-- placement_tabbed is a SUBSET of placement_inbox, not a sibling of it. A tabbed
-- message did land in the inbox — the tab is a sub-location inside it — so it
-- counts on the inbox side exactly as it did before this migration, and every
-- existing threshold, minimum-sample gate and rate keeps the value it had for the
-- same evidence. That is load-bearing, not incidental: making placement_inbox
-- strict would silently drop every Gmail Promotions landing out of the placement
-- denominator, push the mailbox under MinPlacementSamples, and demote it to
-- `unknown` because of a vocabulary change that observed nothing new.
--
-- placement_tab_capable is the tabbed rate's OWN denominator: inbox-side
-- placements whose reader could have reported a category. Counted separately
-- because tabs are structurally undetectable over IMAP, and pooling observations
-- that can never report one would dilute the rate toward zero — making an untested
-- pool read clean, which is exactly the defect the bounce denominators had.
ALTER TABLE warmup_signal_snapshots
    ADD COLUMN placement_tabbed      INT NOT NULL DEFAULT 0 CHECK (placement_tabbed >= 0),
    ADD COLUMN placement_tab_capable INT NOT NULL DEFAULT 0 CHECK (placement_tab_capable >= 0);

-- A tab cannot be positively identified by a reader that could not see labels, so
-- the numerator can never exceed its denominator. Structural rather than trusted
-- to the writer, because the failure it prevents is a rate above 100% on an
-- operator's screen — the "numerator past the denominator" shape this subsystem
-- has already shipped once. It fails CLOSED: a snapshot refresh that would violate
-- it aborts, and the sweep then declines to promote on stale evidence.
ALTER TABLE warmup_signal_snapshots
    ADD CONSTRAINT warmup_signal_snapshots_tabbed_within_capable
    CHECK (placement_tabbed <= placement_tab_capable);
