-- Warmup content-version attribution, Phase 2. See
-- docs/warmup-reputation-network-design.md §8 ("one content generator version
-- correlates with spam") and §12 Phase 2.
--
-- A placement observation records WHERE a warmup message landed but not WHICH
-- content produced it, so "this thread template lands in spam" and "this mailbox is
-- degrading" arrive as the same signal — and they call for opposite responses:
-- retire a template, or contain a mailbox. These two columns are the split.
--
-- The identifier is derived in warmup.ContentVersion from the LIBRARY TEMPLATE the
-- send used, not from the body that went out (library content is spintax, so the
-- expanded body differs per send and fingerprinting it would mint a version per
-- message) and not from the template's position in the library (the corpus grows by
-- insertion, which would silently re-label every existing thread's history).

-- The send RECORDS what it sent. This is the origin of the value: warmup_sends is
-- the only place that knows which (template, turn) a message carried, and it knows
-- it before delivery.
ALTER TABLE warmup_sends
    ADD COLUMN IF NOT EXISTS content_version TEXT NOT NULL DEFAULT '';

-- The observation CARRIES it, copied from the send inside
-- RecordWarmupPlacementObservation's own INSERT ... SELECT rather than passed in by
-- the caller. Two reasons, and the second is the load-bearing one:
--
--   1. It cannot disagree with the send. One statement reads and writes it, so
--      there is no second lifecycle to keep in step — the shape every repeated
--      defect in this subsystem has taken.
--   2. It survives the send being deleted. warmup_observations' FK to warmup_sends
--      is ON DELETE SET NULL (000054), because a CHECK demanding the anchor would
--      make any mailbox with warmup history undeletable. So an observation
--      outlives its send row, and an aggregation that JOINed to warmup_sends for
--      this value would silently drop those rows — the per-version counters would
--      stop summing to the pooled total they were split out of, which is exactly
--      the property that makes a split trustworthy.
--
-- The same reasoning destination_esp (000062) and tab_capable (000060) are stored
-- on the observation for: a value re-derived at read time lets later history
-- re-bucket earlier history, and a report that silently re-buckets its own history
-- is worse than no report.
ALTER TABLE warmup_observations
    ADD COLUMN IF NOT EXISTS content_version TEXT NOT NULL DEFAULT '';

-- '' is "not attributed", and it is the DEFAULT so that every pre-existing row —
-- and every caller written before this slice, including the helpers that seed
-- evidence in tests — keeps working instead of aborting a receipt on a CHECK.
-- Aborting would take the receipt, the placement and both stat writes with it, over
-- a column that gates nothing.
--
-- The CHECK is on SHAPE, because the vocabulary here cannot be closed the way
-- destination_esp's could — the digest space is open by construction and a new
-- content generator gets a new scheme prefix without a migration. It is still worth
-- constraining, for destination_esp's reason: the read side GROUPs BY this column,
-- so a bad value does not fail loudly, it becomes a version of its own. The
-- fixed-width lowercase-hex digest is what makes the guard real — a uuid, a
-- timestamp, an expanded message body, a bare counter or anything else that varies
-- per send cannot satisfy it, and those are precisely the mistakes that would
-- shatter one template into thousands of unreadable one-sample rows.
ALTER TABLE warmup_sends
    ADD CONSTRAINT warmup_sends_content_version_check
    CHECK (content_version = '' OR content_version ~ '^[a-z0-9]+:[0-9a-f]{16}$');

ALTER TABLE warmup_observations
    ADD CONSTRAINT warmup_observations_content_version_check
    CHECK (content_version = '' OR content_version ~ '^[a-z0-9]+:[0-9a-f]{16}$');
