-- Warmup identity facts, Phase 2 slice B. See
-- docs/superpowers/specs/2026-08-14-warmup-identity-extraction-design.md.
--
-- Reputation is currently attached to a mailbox and its organizational domain,
-- which says a mailbox is degrading and not WHY. These five columns record the
-- sending identity each observation carried — the DKIM signing domain, the
-- return-path domain — and the authentication verdicts the RECEIVING provider
-- reached on it, so a later slice can say "these three domains fail through one
-- relay" instead of firing three unrelated per-mailbox alarms.
--
-- They live on the observation rather than in a warmup_identity_facts table of
-- their own. The observation already exists and is already written in the same
-- transaction; a second row with its own lifecycle to keep in step with it is the
-- "two things that must agree" shape every repeated defect in this subsystem has
-- taken. A separate table earns its place when an identity outlives the
-- observation that saw it — Phase 2's later slices, not this one.
--
-- NOTHING GATES ON THESE COLUMNS, and that is a decision rather than an omission.
-- The verdicts are 'unknown' for every provider that does not stamp results, so
-- gating would penalise a whole provider class for our inability to observe it —
-- the same argument that keeps the tabbed rate advisory. Authentication posture is
-- already gated, correctly and separately, by sending_domains and the pending_auth
-- lane, which act on DNS we can verify ourselves rather than on a header a message
-- carried. Before wiring a threshold to dmarc_result, read design §7.
ALTER TABLE warmup_observations
    ADD COLUMN IF NOT EXISTS dkim_domain        TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS return_path_domain TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS spf_result         TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS dkim_result        TEXT NOT NULL DEFAULT 'unknown',
    ADD COLUMN IF NOT EXISTS dmarc_result       TEXT NOT NULL DEFAULT 'unknown';

-- Empty string, not NULL, for the domains: absent and unparseable are the same
-- fact here, and one representation avoids a three-way condition at every read.
-- 'unknown' plays the same role for the verdicts, and is the DEFAULT so that every
-- pre-existing row — and every row a caller that predates this migration writes —
-- states honestly that nothing was observed, rather than defaulting to a verdict it
-- never earned.
--
-- The verdict vocabulary is closed by a CHECK because these columns are a policy
-- input for a later slice, not a log line: a receiver's raw 'softfail' or
-- 'temperror' stored verbatim would be a value that slice has to re-interpret,
-- and the writers normalise to this set instead (coreapi's verdictOrUnknown).
ALTER TABLE warmup_observations
    ADD CONSTRAINT warmup_observations_auth_results_check
    CHECK (spf_result   IN ('pass','fail','neutral','none','unknown')
       AND dkim_result  IN ('pass','fail','neutral','none','unknown')
       AND dmarc_result IN ('pass','fail','neutral','none','unknown'));
