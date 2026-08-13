-- warmup_participants has carried `health_state` beside `health_reason` since the
-- engine shipped, but 000055 added `lane` with no matching `lane_reason` — the
-- explanation lived only on the transition trail. GET /warmup/overview therefore
-- had nowhere to read it from, and the API schema has required `lane_reason` since
-- lanes shipped, so the field was simply absent from the response: the SPA received
-- undefined, fell back to the safe default, and confidently rendered a "probation"
-- badge for every participant whose lane was actually something else.
--
-- Storing it beside the state it explains, rather than deriving it from the newest
-- transition on every read, keeps the two axes symmetric and keeps the overview a
-- single query over the pool.
ALTER TABLE warmup_participants
    ADD COLUMN lane_reason TEXT NOT NULL DEFAULT '';

-- Seed from the trail so existing participants explain themselves immediately
-- rather than waiting for their next transition. A participant that has never
-- transitioned keeps the empty default, which reads as "no explanation recorded"
-- — true, and distinguishable from a real reason.
UPDATE warmup_participants p
SET lane_reason = COALESCE((
        SELECT t.lane_reason
        FROM warmup_state_transitions t
        WHERE t.workspace_id = p.workspace_id
          AND t.mailbox_id = p.mailbox_id
          AND t.to_lane = p.lane
          AND t.lane_reason IS NOT NULL
        ORDER BY t.created_at DESC
        LIMIT 1
    ), '');

-- Externally ASSERTED hard bounces, kept apart from the ones Inroad observed
-- itself. Both are real evidence, but they carry different authority:
-- sequence_enrollments rows come from our own DSN parser, whereas
-- deliverability_events rows are whatever a holder of deliverability:write
-- posted. That scope exists precisely so an ingest credential cannot mutate
-- campaigns, and pooling the two arms handed it exactly that — roughly seven
-- forged events quarantined a mailbox and withheld its whole domain for 72h,
-- which the tenant then could not clear.
--
-- Split so the policy can treat asserted evidence the way invariant 39 already
-- treats the DNS advisory: it may reduce volume, it may not contain.
ALTER TABLE warmup_signal_snapshots
    ADD COLUMN campaign_asserted_hard_bounces INT NOT NULL DEFAULT 0
        CHECK (campaign_asserted_hard_bounces >= 0);
