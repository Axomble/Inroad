-- Phase 1 of the reputation network: pool eligibility becomes its own axis,
-- signals are materialized per workspace instead of recomputed per participant,
-- and bounce classes are distinguished so a greylist stops reading as a hard
-- bounce. See docs/superpowers/specs/2026-08-12-warmup-reputation-phase-1-design.md.

-- Pool lane, separate from health_state. health_state answers "how does this
-- mailbox's outbound mail perform"; lane answers "who may it exchange traffic
-- with". Phase 0 overloaded one column with both questions, which is why a
-- mailbox with no evidence and a mailbox with bad evidence were indistinguishable
-- to the pool. Default probation: a new participant has proven nothing.
ALTER TABLE warmup_participants
    ADD COLUMN lane TEXT NOT NULL DEFAULT 'probation';
ALTER TABLE warmup_participants
    ADD CONSTRAINT warmup_participants_lane_check
    CHECK (lane IN ('pending_auth','probation','healthy','watch','recovery','quarantine','blocked'));

-- Hard/soft bounce discrimination. security.md states that provider bounce feeds
-- include soft bounces (full mailbox, greylisting), and Phase 0 summed that feed
-- into a rate it reported as "hard-bounce rate above 10%" — so a normal week of
-- greylisting could pause a healthy mailbox for 72 hours. Existing rows stay
-- 'unknown' and are excluded from the hard-bounce numerator: under-counting
-- historical data is the safe direction, over-counting pauses innocent mailboxes.
ALTER TABLE deliverability_events
    ADD COLUMN bounce_class TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE deliverability_events
    ADD CONSTRAINT deliverability_events_bounce_class_check
    CHECK (bounce_class IN ('hard','soft','unknown'));

-- Materialized signals, one row per participant, recomputed once per workspace
-- per sweep. Phase 0 recomputed eight correlated subqueries PER PARTICIPANT on
-- every five-minute tick, including an arm over sequence_enrollments that no
-- index could serve. Phase 1 adds subjects to fan out over, so the fan-out is
-- replaced rather than extended.
--
-- Campaign and warmup counts are deliberately SEPARATE columns. Phase 0 summed
-- them into one bounce denominator, and warmup traffic (synthetic mail between
-- the operator's own mailboxes, which essentially never hard-bounces) diluted it
-- below the thresholds it was meant to trip: 20 hard bounces on 200 campaign
-- sends is a 10% rate, but 20/(200+1200) reads as 1.4%. Unlike denominators are
-- never summed here; the policy evaluates each population against its own gate.
CREATE TABLE warmup_signal_snapshots (
    workspace_id            UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    mailbox_id              UUID NOT NULL,
    computed_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    placement_inbox         INT NOT NULL DEFAULT 0 CHECK (placement_inbox >= 0),
    placement_spam          INT NOT NULL DEFAULT 0 CHECK (placement_spam >= 0),
    campaign_delivered      INT NOT NULL DEFAULT 0 CHECK (campaign_delivered >= 0),
    campaign_hard_bounces   INT NOT NULL DEFAULT 0 CHECK (campaign_hard_bounces >= 0),
    campaign_complaints     INT NOT NULL DEFAULT 0 CHECK (campaign_complaints >= 0),
    warmup_delivered        INT NOT NULL DEFAULT 0 CHECK (warmup_delivered >= 0),
    warmup_hard_bounces     INT NOT NULL DEFAULT 0 CHECK (warmup_hard_bounces >= 0),
    -- Observer-side only: how much forged warmup traffic THIS mailbox received.
    -- Never attributed to a claimed sender (see the invalid_token CHECKs below).
    observer_token_failures INT NOT NULL DEFAULT 0 CHECK (observer_token_failures >= 0),
    -- NULL when the participant has no evidence at all. Drives the staleness rule:
    -- a snapshot older than the freshness window is treated as no evidence, never
    -- as health.
    newest_evidence_at      TIMESTAMPTZ,
    PRIMARY KEY (workspace_id, mailbox_id),
    FOREIGN KEY (mailbox_id, workspace_id)
        REFERENCES mailboxes(id, workspace_id) ON DELETE CASCADE
);

-- Lane history alongside health history. Deliberately NULLABLE rather than
-- defaulted: rows written before lanes existed genuinely had no lane, and
-- fabricating 'probation' for them would put a claim in an append-only audit
-- trail that was never true. Every new row sets both — enforced by the writer,
-- which is the same place the send anchor is enforced on observations.
-- Each axis carries its OWN explanation. One reason_code cannot serve two
-- independent decisions: when health says "spam placement above the pause
-- threshold" and the lane says "quarantined", collapsing them into one slot
-- destroys whichever loses, and a transition that cannot explain itself is
-- exactly what Phase 0 shipped. reason_code/reason keep their Phase 0 meaning
-- (the health axis); the lane columns are additive.
ALTER TABLE warmup_state_transitions
    ADD COLUMN from_lane        TEXT,
    ADD COLUMN to_lane          TEXT,
    ADD COLUMN lane_reason_code TEXT,
    ADD COLUMN lane_reason      TEXT;
ALTER TABLE warmup_state_transitions
    ADD CONSTRAINT warmup_state_transitions_from_lane_check
    CHECK (from_lane IS NULL OR from_lane IN ('pending_auth','probation','healthy','watch','recovery','quarantine','blocked'));
ALTER TABLE warmup_state_transitions
    ADD CONSTRAINT warmup_state_transitions_to_lane_check
    CHECK (to_lane IS NULL OR to_lane IN ('pending_auth','probation','healthy','watch','recovery','quarantine','blocked'));
-- Symmetric with reason_code's non-empty CHECK on the health axis. That guarantee
-- turned out to be load-bearing rather than decorative: an unexplained decision
-- aborts the atomic statement it travels in, so the lane axis gets the same
-- structural enforcement rather than relying on the writer to remember.
ALTER TABLE warmup_state_transitions
    ADD CONSTRAINT warmup_state_transitions_lane_reason_code_check
    CHECK (lane_reason_code IS NULL OR btrim(lane_reason_code) <> '');

-- Make the invalid-token safeguard STRUCTURAL. An unauthenticated token may claim
-- any sender, so trusting the claim would let anyone throttle a mailbox they do
-- not own by emailing it three times. Phase 0 relied on one INSERT remembering to
-- omit mailbox_id and hardcode attribution_trusted = false; these constraints
-- mean the database refuses the unsafe row instead. Existing rows already satisfy
-- both, because that INSERT is the only writer of this kind.
ALTER TABLE warmup_observations
    ADD CONSTRAINT warmup_observations_invalid_token_untrusted
    CHECK (kind <> 'invalid_token' OR attribution_trusted = false);
ALTER TABLE warmup_observations
    ADD CONSTRAINT warmup_observations_invalid_token_unattributed
    CHECK (kind <> 'invalid_token' OR mailbox_id IS NULL);

-- The warmup hard-bounce lookup runs on EVERY inbound DSN, before the campaign
-- lookup, and warmup_sends had no index on message_id — so every campaign bounce
-- scanned every warmup send the workspace had ever made. Mirrors idx_sends_message_id.
CREATE INDEX idx_warmup_sends_message_id
    ON warmup_sends (workspace_id, message_id) WHERE message_id <> '';

-- Serves the campaign hard-bounce aggregation's enrollment arm, which binds
-- workspace_id, stop_reason and stopped_at but not campaign_id, so the existing
-- (campaign_id, stopped_at) index could not be used.
CREATE INDEX idx_sequence_enrollments_bounced
    ON sequence_enrollments (workspace_id, stopped_at) WHERE stop_reason = 'bounced';

-- Lane backfill: evidence decides, reusing the observation trail Phase 0 built.
-- Nothing is grandfathered into the healthy lane on the strength of a health
-- state the old, diluted denominators produced.
--
-- Ordering matters: already-degraded participants go to quarantine FIRST, so a
-- throttled mailbox cannot be handed probation's 5/day allowance as though that
-- were a restriction. Only then does clean evidence promote the remainder.
UPDATE warmup_participants
SET lane = CASE
    WHEN health_state IN ('paused','throttled') THEN 'quarantine'
    WHEN health_state = 'watch' THEN 'watch'
    WHEN (
        SELECT COUNT(*)
        FROM warmup_observations o
        WHERE o.workspace_id = warmup_participants.workspace_id
          AND o.mailbox_id = warmup_participants.mailbox_id
          AND o.kind = 'placement'
          AND o.attribution_trusted
          AND o.observed_at >= now() - interval '7 days'
    ) >= 20
    AND COALESCE((
        SELECT COUNT(*) FILTER (WHERE o.placement = 'spam')::numeric
             / NULLIF(COUNT(*) FILTER (WHERE o.placement IN ('inbox','spam')), 0)
        FROM warmup_observations o
        WHERE o.workspace_id = warmup_participants.workspace_id
          AND o.mailbox_id = warmup_participants.mailbox_id
          AND o.kind = 'placement'
          AND o.attribution_trusted
          AND o.observed_at >= now() - interval '7 days'
    ), 1) <= 0.15
    THEN 'healthy'
    ELSE 'probation'
END,
updated_at = now();

-- The quarantine cooldown is derived from the newest transition that MOVED a
-- participant into quarantine. The backfill above sets lane directly, so without a
-- matching row a backfilled quarantine has no entry timestamp — and a NULL entry
-- time is treated as "cooldown has not elapsed", holding the mailbox forever. Write
-- the entry rows the derivation expects.
--
-- created_at is now(): the cooldown starts at DEPLOY time, not retroactively. The
-- alternative would credit a mailbox for time served under a policy that did not
-- exist, which is the "time alone reinstates a participant" failure this phase
-- exists to prevent.
INSERT INTO warmup_state_transitions (
    workspace_id, mailbox_id, from_state, to_state, from_lane, to_lane,
    reason_code, reason, lane_reason_code, lane_reason,
    placement_samples, spam_rate, bounce_samples, bounce_rate,
    complaint_samples, complaint_rate, invalid_tokens, policy_version
)
SELECT p.workspace_id, p.mailbox_id, p.health_state, p.health_state, 'probation', 'quarantine',
       'health_unchanged', 'lane backfill: health was already degraded',
       'lane_quarantined', 'backfilled into quarantine from a degraded health state',
       0, 0, 0, 0, 0, 0, 0, 'warmup-phase1-v1'
FROM warmup_participants p
WHERE p.lane = 'quarantine';
