-- Warmup control-plane persistence (spec §3). Every tenant query is
-- workspace_id-pinned (belt-and-braces on the unguessable mailbox/workspace
-- UUIDs), mirroring queries/mailbox.sql. The send/receipt/thread/health query
-- surface belongs to later worker steps and is intentionally not here yet.

-- name: UpsertWarmupParticipant :one
-- Enable warmup for a mailbox or update its ramp settings. On re-enable the row
-- is flipped back to enabled=true. Self-enforcing tenancy (defense in depth): the
-- base INSERT is an INSERT ... SELECT that emits a row ONLY when the mailbox truly
-- belongs to the workspace, so a first upsert with a foreign (mailbox, workspace)
-- pair inserts zero rows and RETURNING yields pgx.ErrNoRows — never binding another
-- tenant's mailbox into this workspace. The ON CONFLICT UPDATE is likewise
-- workspace-pinned, so a cross-workspace collision on an existing row updates
-- nothing and returns no row. The caller maps that ErrNoRows to a domain sentinel.
-- Re-entry does NOT clear containment. Disabling deletes the row, so a fresh
-- INSERT would otherwise fall back to lane='probation' — and two calls any member
-- with mailboxes:write can make (DELETE then PUT) would release a quarantined or
-- blocked mailbox into a lane that may send and may take new campaign leads.
-- warmup_state_transitions survives the delete, so the last recorded lane is
-- restored when it was a sealed one. Only quarantine and blocked are carried
-- forward: a mailbox that legitimately left them has a later transition saying so.
INSERT INTO warmup_participants (
    mailbox_id, workspace_id,
    start_volume, max_volume, ramp_increment, reply_rate, lane
)
SELECT $1, $2, $3, $4, $5, $6,
       COALESCE((
           SELECT CASE WHEN t.to_lane IN ('quarantine','blocked') THEN t.to_lane END
           FROM warmup_state_transitions t
           WHERE t.workspace_id = $2 AND t.mailbox_id = $1
             AND t.to_lane IS NOT NULL
           ORDER BY t.created_at DESC
           LIMIT 1
       ), 'probation')
FROM mailboxes WHERE id = $1 AND workspace_id = $2
ON CONFLICT (mailbox_id) DO UPDATE SET
    enabled        = true,
    start_volume   = EXCLUDED.start_volume,
    max_volume     = EXCLUDED.max_volume,
    ramp_increment = EXCLUDED.ramp_increment,
    reply_rate     = EXCLUDED.reply_rate,
    updated_at     = now()
WHERE warmup_participants.workspace_id = $2
RETURNING *;

-- name: GetWarmupParticipant :one
SELECT * FROM warmup_participants
WHERE mailbox_id = $1 AND workspace_id = $2;

-- name: DisableWarmupParticipant :execrows
-- Disabling deletes the row (spec §10: DELETE /mailboxes/{id}/warmup -> 204).
DELETE FROM warmup_participants
WHERE mailbox_id = $1 AND workspace_id = $2;

-- name: WarmupMailboxInWorkspace :one
-- Does this mailbox belong to the caller's workspace? The transition history is
-- readable for a mailbox that is NOT (or is no longer) a participant — the trail
-- outlives the participant row, which is how containment survives a
-- disable/re-enable — so participation cannot be the 404 test. Ownership is, and
-- it is pinned on the workspace from the JWT, never a caller-supplied value.
SELECT EXISTS (
    SELECT 1 FROM mailboxes WHERE id = $1 AND workspace_id = $2
) AS in_workspace;

-- name: ListWarmupTransitions :many
-- One mailbox's automated state-change history, newest first, workspace-pinned.
-- Serves GET /warmup/mailboxes/{mailbox_id}/transitions: every row already names
-- the metric, sample size and threshold that produced it, which is what lets an
-- operator answer "why is this mailbox here and what clears it" without reading
-- logs.
--
-- id breaks a created_at tie so paging is deterministic; the ordering otherwise
-- matches idx_warmup_state_transitions_mailbox (workspace_id, mailbox_id,
-- created_at DESC) exactly, so this is an index scan with a LIMIT rather than a
-- sort of the whole history.
SELECT id, created_at, from_state, to_state, reason_code, reason,
       from_lane, to_lane, lane_reason_code, lane_reason,
       placement_samples, spam_rate,
       bounce_population, bounce_samples, bounce_rate,
       complaint_samples, complaint_rate, invalid_tokens, policy_version
FROM warmup_state_transitions
WHERE workspace_id = $1 AND mailbox_id = $2
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: CountEnabledParticipants :one
SELECT count(*) FROM warmup_participants
WHERE workspace_id = $1 AND enabled;

-- Day-boundary convention: the daily-stats reads below anchor their windows on
-- CURRENT_DATE. The DB session runs in UTC, so "today"/"last N days" are UTC-day
-- boundaries, not any recipient-local day. The future stats WRITER (C4) MUST
-- aggregate on the same UTC boundary so writes and reads agree. (Engagement
-- waking-hours scheduling uses recipient-local time separately; daily_stats is
-- strictly UTC.)

-- name: GetWarmupDailyStats :many
-- One mailbox's last 30 UTC days of counters, oldest first, for the detail series.
SELECT * FROM warmup_daily_stats
WHERE mailbox_id = $1 AND workspace_id = $2
  AND day >= CURRENT_DATE - 29
ORDER BY day ASC;

-- name: GetWarmupSentToday :one
-- Today's (UTC) sent count for one mailbox. Aggregated so a missing day row
-- yields 0.
SELECT COALESCE(SUM(sent), 0)::int AS sent
FROM warmup_daily_stats
WHERE mailbox_id = $1 AND workspace_id = $2
  AND day = CURRENT_DATE;

-- name: ListWarmupOverviewRows :many
-- One workspace-pinned row per participant for GET /warmup/overview: the
-- participant's ramp/health fields, the mailbox email (INNER join — a participant
-- always maps to a live mailbox), the trailing-7-UTC-day SENDER placement sums
-- (inbox/spam — the deliverability signal, §4/§8), and today's (UTC) sent count.
-- The two stat rollups are LEFT-joined subqueries so a participant with no stats
-- yet yields zeros, and everything resolves in ONE query (no N+1 over the pool).
-- The two subqueries and the outer WHERE are all workspace-pinned on $1.
SELECT
    p.mailbox_id, p.enabled, p.start_volume, p.max_volume, p.ramp_increment,
    p.reply_rate, p.started_at, p.health_state, p.health_reason,
    -- The POOL ELIGIBILITY axis, alongside the reputation axis above. The schema
    -- has required both since lanes shipped, but this query never selected them,
    -- so the field was absent from the JSON, arrived as undefined in the SPA, and
    -- every mailbox fell back to the "probation" badge — a wrong lane shown
    -- confidently for every participant that was not actually in probation.
    p.lane, p.lane_reason,
    m.email,
    COALESCE(wk.inbox, 0)::bigint AS inbox_7d,
    COALESCE(wk.spam, 0)::bigint  AS spam_7d,
    COALESCE(td.sent, 0)::int     AS today_sent
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id AND m.workspace_id = p.workspace_id
LEFT JOIN (
    SELECT o.mailbox_id,
           count(*) FILTER (WHERE o.placement = 'inbox') AS inbox,
           count(*) FILTER (WHERE o.placement = 'spam') AS spam
    FROM warmup_observations o
    WHERE o.workspace_id = $1
      AND o.kind = 'placement'
      AND o.attribution_trusted
      AND o.observed_at >= now() - interval '7 days'
    GROUP BY o.mailbox_id
) wk ON wk.mailbox_id = p.mailbox_id
LEFT JOIN (
    SELECT s.mailbox_id, s.sent
    FROM warmup_daily_stats s
    WHERE s.workspace_id = $1 AND s.day = CURRENT_DATE
) td ON td.mailbox_id = p.mailbox_id
WHERE p.workspace_id = $1
ORDER BY p.created_at DESC;

-- ============================================================================
-- Send path (spec §4/§6) — the control⇄execution seam's warmup read/claim
-- surface. Every statement is workspace_id-pinned; every INSERT of a
-- (mailbox/thread, workspace) row is SELF-ENFORCING (INSERT ... SELECT FROM
-- mailboxes WHERE id=$ AND workspace_id=$) so a foreign pairing writes zero rows.
-- ============================================================================

-- name: GetWarmupSenderBundle :one
-- Everything GetWarmupSendJob needs about the FROM mailbox: its participant ramp
-- config, health and lane, and its decrypted-at-caller transport columns.
-- workspace-pinned (belt-and-braces on the unguessable mailbox UUID); a foreign
-- pair yields no row.
SELECT p.workspace_id, p.enabled, p.start_volume, p.max_volume, p.ramp_increment,
       p.reply_rate, p.started_at, p.health_state, p.lane, p.paused_until,
       -- The lease expiry is minted HERE, from the database clock, because
       -- ClaimWarmupSend compares it against the database clock. Computing it in
       -- Go would put app/DB skew on both ends of a security check — the exact
       -- mistake that made the Phase 1 freshness rule silently always-true.
       (now() + make_interval(secs => sqlc.arg(lease_seconds)::int))::timestamptz AS lease_expires_at,
       m.provider, m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.allow_plaintext
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id
WHERE p.mailbox_id = $1 AND p.workspace_id = $2;

-- ----------------------------------------------------------------------------
-- Lane compatibility (design §6.1, acceptance criterion 1). Every partner query
-- below enforces the same two rules:
--
--     AND partner.lane = sender.lane
--     AND sender.lane NOT IN ('pending_auth','quarantine','blocked')
--
-- With no sentinel lane, same-lane IS the whole rule — simple enough to be
-- provable, which is the point: a healthy customer mailbox never sends to, and
-- never receives from, a probation, recovery, watch, quarantined, blocked or
-- unauthenticated peer. Because the two lanes are equal, excluding the sealed
-- lanes on the SENDER excludes them on the partner too.
--
-- The sender's own participant row is joined in (rather than passed as a
-- parameter) so the lane the comparison uses is the one committed in the database
-- at query time; a caller cannot widen its own eligibility by sending a lane.
-- ----------------------------------------------------------------------------

-- name: CountEligibleWarmupPartners :one
SELECT count(*) FROM warmup_participants p
JOIN warmup_participants sender
  ON sender.mailbox_id = $2 AND sender.workspace_id = $1
WHERE p.workspace_id = $1
  AND p.mailbox_id <> $2
  AND p.enabled
  AND p.health_state <> 'paused'
  AND (p.paused_until IS NULL OR p.paused_until <= now())
  AND p.lane = sender.lane
  AND sender.lane NOT IN ('pending_auth','quarantine','blocked');

-- ----------------------------------------------------------------------------
-- The symmetric pair budget (pair-leases design §6). ONE budget per pair per UTC
-- day, drawn down by BOTH directions and BOTH kinds — new sends and engagement
-- replies alike. What bounds reputation exposure is what the two mailboxes
-- exchange, which is the number a destination provider sees; counting only
-- from→to let real per-pair volume run to roughly twice the nominal cap, because
-- the partner's own sends back (and every reply in either direction) were free.
--
-- The count keys on the GENERATED pair_key, not on an OR of the two directions:
-- an OR makes Postgres union two scans, and this counter runs inside a LATERAL
-- over every candidate partner on the hottest read in the engine. is_reply is
-- deliberately NOT filtered — a reply costs the pair the same exposure a new
-- thread does.
--
-- The day is bounded on created_at because a claimed-but-unsent ('sending') row
-- has sent_at IS NULL: bounding on sent_at would drop in-flight sends from the
-- count and let two concurrent workers overrun the cap. idx_warmup_sends_pair_day
-- is (workspace_id, pair_key, created_at) WHERE status IN ('sending','sent') and
-- serves this index-only.
-- ----------------------------------------------------------------------------

-- name: SelectWarmupPartner :one
-- Pick ONE eligible warmup partner for a sender: a DIFFERENT, enabled, non-paused
-- participant in the SAME workspace, preferring one not recently paired with the
-- sender. Ordering: least-recently-active shared thread first (a never-paired
-- partner sorts on 'epoch', so it wins), tie-broken deterministically by
-- mailbox_id so partner spread is stable and reproducible. workspace-pinned AND
-- lane-pinned (see the lane-compatibility note above); a workspace with <2
-- eligible SAME-LANE participants returns no row. sent_today is the SYMMETRIC
-- pair budget (see the note above), so it also orders the spread by what the pair
-- has actually exchanged rather than by what this sender happened to send.
WITH candidates AS (
    SELECT p.mailbox_id, m.email, m.display_name,
           COALESCE(pair.last_pair_at, 'epoch'::timestamptz) AS last_pair_at,
           COALESCE(pair.sent_today, 0)::bigint AS sent_today
    FROM warmup_participants p
    JOIN mailboxes m ON m.id = p.mailbox_id AND m.workspace_id = p.workspace_id
    JOIN warmup_participants sender
      ON sender.mailbox_id = $2 AND sender.workspace_id = $1
    LEFT JOIN LATERAL (
        SELECT
            (SELECT MAX(t.last_activity_at)
             FROM warmup_threads t
             WHERE t.workspace_id = $1
               AND ((t.sender_mailbox = $2 AND t.partner_mailbox = p.mailbox_id)
                 OR (t.sender_mailbox = p.mailbox_id AND t.partner_mailbox = $2))) AS last_pair_at,
            (SELECT COUNT(*)
             FROM warmup_sends s
             WHERE s.workspace_id = $1
               AND s.pair_key = least($2::uuid::text, p.mailbox_id::text) || ':' ||
                                greatest($2::uuid::text, p.mailbox_id::text)
               AND s.status IN ('sending','sent')
               AND s.created_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') AS sent_today
    ) pair ON true
    WHERE p.workspace_id = $1
      AND p.mailbox_id <> $2
      AND p.enabled
      AND p.health_state <> 'paused'
      AND (p.paused_until IS NULL OR p.paused_until <= now())
      AND p.lane = sender.lane
      AND sender.lane NOT IN ('pending_auth','quarantine','blocked')
)
SELECT mailbox_id, email, display_name
FROM candidates c
WHERE c.sent_today < sqlc.arg(max_pair_sends)::int
  AND (
      c.last_pair_at <= sqlc.arg(cooldown_since)::timestamptz
      OR NOT EXISTS (
          SELECT 1 FROM candidates fresh
          WHERE fresh.sent_today < sqlc.arg(max_pair_sends)::int
            AND fresh.last_pair_at <= sqlc.arg(cooldown_since)::timestamptz
      )
  )
ORDER BY c.sent_today ASC, c.last_pair_at ASC, c.mailbox_id ASC
LIMIT 1;

-- name: SelectWarmupReplyPartner :one
-- Pick ONE eligible warmup partner for a sender that ALSO has an OPEN,
-- NON-EXHAUSTED shared thread the sender can reply INTO — so a wanted reply
-- actually lands on a repliable partner instead of falling through to a new
-- thread (the reply_rate under-realization the recency-spread SelectWarmupPartner
-- causes: its least-recently-active pick is the LEAST likely to have an open
-- thread). Same eligibility as SelectWarmupPartner (DIFFERENT, enabled,
-- non-paused, SAME workspace). "Open + repliable" is judged on the pair's LATEST
-- thread (the one GetOpenWarmupThread would reply into): its turn must be
-- >= 1 (its opener already sent, so root_message_id is set for In-Reply-To) and
-- < @max_turn (the library's MaxContentTurns — a thread at/over it is exhausted
-- for EVERY library thread). @max_turn is a COARSE bound: a shorter thread can be
-- exhausted below it, so the caller still confirms with warmup.Reply and, on a
-- miss, falls back to the new-thread path. Ordered by that thread's
-- last_activity_at ASC so replies still spread across repliable partners
-- (least-recently-active first, matching SelectWarmupPartner's spread), tie-broken
-- by mailbox_id for determinism. workspace-pinned; no repliable partner → no row.
-- The @max_pair_sends gate is the SAME symmetric per-pair-per-day budget
-- SelectWarmupPartner draws on (see the note above it), so choosing to reply
-- cannot buy a pair extra volume the new-thread path would have been refused.
SELECT p.mailbox_id, m.email, m.display_name,
       t.id AS thread_id, t.content_key, t.turn, t.root_message_id
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id
JOIN warmup_participants sender
  ON sender.mailbox_id = $2 AND sender.workspace_id = $1
JOIN LATERAL (
    SELECT th.id, th.content_key, th.turn, th.root_message_id, th.last_activity_at
    FROM warmup_threads th
    WHERE th.workspace_id = $1
      AND ((th.sender_mailbox = $2 AND th.partner_mailbox = p.mailbox_id)
        OR (th.sender_mailbox = p.mailbox_id AND th.partner_mailbox = $2))
    ORDER BY th.last_activity_at DESC
    LIMIT 1
) t ON true
WHERE p.workspace_id = $1
  AND p.mailbox_id <> $2
  AND p.enabled
  AND p.health_state <> 'paused'
  AND (p.paused_until IS NULL OR p.paused_until <= now())
  AND p.lane = sender.lane
  AND sender.lane NOT IN ('pending_auth','quarantine','blocked')
  AND t.turn >= 1
  AND t.turn < sqlc.arg(max_turn)::int
  AND (SELECT COUNT(*) FROM warmup_sends s
       WHERE s.workspace_id = $1
         AND s.pair_key = least($2::uuid::text, p.mailbox_id::text) || ':' ||
                          greatest($2::uuid::text, p.mailbox_id::text)
         AND s.status IN ('sending','sent')
         AND s.created_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc')
      < sqlc.arg(max_pair_sends)::int
ORDER BY t.last_activity_at ASC, p.mailbox_id ASC
LIMIT 1;

-- name: GetOpenWarmupThread :one
-- The most recent thread between (sender, partner) in either direction, used to
-- decide whether the next send can reply into an existing conversation. The caller
-- checks turn against the resolved library content to know if a reply turn
-- remains. workspace-pinned.
SELECT id, workspace_id, sender_mailbox, partner_mailbox, subject,
       root_message_id, turn, content_key, last_activity_at, created_at
FROM warmup_threads
WHERE workspace_id = $1
  AND ((sender_mailbox = $2 AND partner_mailbox = $3)
    OR (sender_mailbox = $3 AND partner_mailbox = $2))
ORDER BY last_activity_at DESC
LIMIT 1;

-- name: InsertWarmupThread :one
-- Open a new synthetic thread. SELF-ENFORCING tenancy: the INSERT ... SELECT emits
-- a row ONLY when the SENDER mailbox truly belongs to the workspace, so a foreign
-- (sender, workspace) pair inserts zero rows and RETURNING yields pgx.ErrNoRows —
-- never binding another tenant's mailbox into a thread. (The partner is validated
-- upstream by SelectWarmupPartner, which is itself workspace-pinned.)
INSERT INTO warmup_threads (workspace_id, sender_mailbox, partner_mailbox, subject, content_key)
SELECT $1, $2, $3, $4, $5
FROM mailboxes WHERE id = $2 AND workspace_id = $1
RETURNING id, workspace_id, sender_mailbox, partner_mailbox, subject,
          root_message_id, turn, content_key, last_activity_at, created_at;

-- name: AdvanceWarmupThread :exec
-- Advance a thread by one turn after a successful send, and record the first
-- message's Message-ID as the thread root on turn 0 (so later replies chain to it
-- via In-Reply-To/References). workspace-pinned.
UPDATE warmup_threads
SET turn = turn + 1,
    root_message_id = CASE WHEN turn = 0 THEN $3 ELSE root_message_id END,
    last_activity_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: ClaimWarmupSend :one
-- Claim one warmup send for delivery (claim-before-send), mirroring ClaimStepSend.
-- The warmup_sends row is the claim: a fresh INSERT wins it ('sending',
-- claimed_at=now()). SELF-ENFORCING tenancy — the INSERT ... SELECT emits a row
-- ONLY when from_mailbox belongs to the workspace, so a foreign pairing inserts
-- nothing and RETURNING yields pgx.ErrNoRows. On conflict (the row already exists)
-- the claim is re-won ONLY when the existing row is 'queued' (released after a
-- retryable failure) or a STALE 'sending' lease (a crashed worker) — never a
-- terminal 'sent'/'failed' nor a FRESH 'sending' another worker owns. RETURNING id
-- yields a row iff we won; zero rows means skip / recover-forward (caller then
-- reads status to distinguish already-'sent' from a fresh-'sending'/terminal skip).
-- workspace_id is pinned on both the insert value and the reclaim WHERE.
--
-- This statement is ALSO the lease revalidation (pair-leases design §5). The
-- caller supplies the lane and policy version the send was DECIDED under and the
-- expiry the decision carries; the claim refuses — zero rows, so the existing
-- ClaimSkip path fires — under any of three drifts. Refusing here rather than in
-- Go is what makes the check unskippable: the row cannot enter 'sending' without
-- passing it.
--
--   1. EXPIRY. @lease_expires_at was minted from the DATABASE clock at issue and
--      is compared against the DATABASE clock here, so no Go clock enters the
--      comparison — the mistake that made the Phase 1 freshness check silently
--      wrong. It bounds the enqueue→pickup window: a backed-up or retrying asynq
--      queue can fire a warmup:send long after its lane was decided. The check is
--      on the expiry the CALLER holds, not on the stored column: a reclaim always
--      arrives with a freshly re-derived decision, and gating it on the row's own
--      (necessarily older) lease would strand a released row forever — it is the
--      same deterministic id every retry and sweep re-derives, so nothing would
--      ever rewrite the expired lease it was refused for.
--   2. LANE DRIFT, against the sender's CURRENT participant lane read here, in
--      SQL, not against a lane the caller asserts. This is acceptance criterion 7
--      and it is why the join is not optional: on a FRESH insert there is no
--      stored lease to compare, so a quarantine landing between decision and claim
--      is only visible in the live row. A sender with no participant row claims
--      nothing, which is correct — it is not in the pool.
--   3. POLICY DRIFT on the stored row, so a threshold change takes effect on the
--      in-flight tail instead of after it drains.
--
-- The sealed-lane exclusion beside the lane comparison is belt-and-braces for
-- invariant 54 (lane isolation holds on EVERY outbound path): matching the live
-- lane alone would let a caller that asserted 'quarantine' claim, because the
-- assertion would be true. warmup.LaneMaySend gates that upstream; this makes the
-- last write before delivery refuse it too.
--
-- Checks 1 and 2 gate the whole statement (a SELECT that yields no row inserts
-- nothing AND resolves no conflict); the stored-vs-current comparisons in the
-- DO UPDATE WHERE add the reclaim dimension, where a lease issued under an older
-- lane or policy may be sitting on the row. A NULL issued_lane /
-- issued_policy_version is a pre-lease row (written before 000057) and passes:
-- those sends predate the lease and must keep working.
INSERT INTO warmup_sends (id, workspace_id, thread_id, from_mailbox, to_mailbox,
                          is_reply, token, status, claimed_at,
                          issued_lane, issued_policy_version, lease_expires_at)
SELECT $1, $2, $3, $4, $5, $6, $7, 'sending', now(),
       sqlc.arg(issued_lane)::text, sqlc.arg(issued_policy_version)::text,
       sqlc.arg(lease_expires_at)::timestamptz
FROM mailboxes m
JOIN warmup_participants sender
  ON sender.mailbox_id = m.id AND sender.workspace_id = m.workspace_id
WHERE m.id = $4 AND m.workspace_id = $2
  AND sqlc.arg(lease_expires_at)::timestamptz > now()
  AND sender.lane = sqlc.arg(issued_lane)::text
  AND sender.lane NOT IN ('pending_auth','quarantine','blocked')
ON CONFLICT (id) DO UPDATE SET
    status = 'sending', claimed_at = now(), last_error = '',
    -- Re-stamp the lease on a reclaim: the row now carries the decision this
    -- attempt is acting on, not the one a crashed worker acted on.
    issued_lane = sqlc.arg(issued_lane)::text,
    issued_policy_version = sqlc.arg(issued_policy_version)::text,
    lease_expires_at = sqlc.arg(lease_expires_at)::timestamptz
    WHERE warmup_sends.workspace_id = $2
      AND (warmup_sends.status = 'queued'
        OR (warmup_sends.status = 'sending'
            AND warmup_sends.claimed_at < now() - make_interval(secs => sqlc.arg(lease_seconds)::int)))
      AND (warmup_sends.issued_lane IS NULL
           OR warmup_sends.issued_lane = sqlc.arg(issued_lane)::text)
      AND (warmup_sends.issued_policy_version IS NULL
           OR warmup_sends.issued_policy_version = sqlc.arg(issued_policy_version)::text)
RETURNING id;

-- name: RecordWarmupSendConstraints :execrows
-- Snapshot the constraints the match was made under (design §4): which lane, the
-- cooldown in force, the pair budget and how much of it remained. Written right
-- after the claim is won, by the winner only — the status='sending' guard keeps a
-- worker that LOST the claim from overwriting the winner's snapshot, and keeps a
-- late write off a row that has already reached a terminal state.
--
-- Deliberately a plain JSONB object with no schema: it exists so a bad match is
-- reproducible in an incident review, and a fixed shape would have to migrate
-- every time the matcher gains an input. Nothing reads it in code, so nothing
-- breaks when its keys change. Rows affected is returned so a caller that cares
-- can log a snapshot that did not land rather than assume it did.
-- workspace-pinned.
UPDATE warmup_sends
SET issued_constraints = sqlc.arg(issued_constraints)
WHERE id = $1 AND workspace_id = $2 AND status = 'sending';

-- name: CountWarmupPairSendsToday :one
-- The symmetric pair budget already spent today by ONE known pair, for the
-- engagement REPLY path — which selects no partner (the receipt fixes both ends)
-- and so cannot get the count from SelectWarmupPartner. Before this, replies
-- consulted no cap at all, which is the larger half of why real per-pair volume
-- ran to about double the nominal cap. Same key, same window and the same
-- both-directions/both-kinds semantics as the selection queries, so the two agree
-- by construction rather than by two implementations staying in step. The
-- argument order does not matter: pair_key is canonical. workspace-pinned.
SELECT COUNT(*) FROM warmup_sends s
WHERE s.workspace_id = $1
  AND s.pair_key = least(sqlc.arg(mailbox_a)::uuid::text, sqlc.arg(mailbox_b)::uuid::text) || ':' ||
                   greatest(sqlc.arg(mailbox_a)::uuid::text, sqlc.arg(mailbox_b)::uuid::text)
  AND s.status IN ('sending','sent')
  AND s.created_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc';

-- name: GetWarmupSendState :one
-- The claimed row's terminal state, for the lost-claim recover-forward decision
-- (a 'sent' row means this exact send already delivered). workspace-pinned.
SELECT status, message_id FROM warmup_sends
WHERE id = $1 AND workspace_id = $2;

-- name: SetWarmupSendSent :execrows
-- Finalize a claimed row to 'sent' + record its Message-ID. Guarded on
-- status='sending' and returns rows affected so the caller advances the thread and
-- bumps the daily counter ONLY when THIS call did the sending→sent transition
-- (idempotent: a re-run over an already-'sent' row affects 0 rows and skips the
-- side effects, never double-counting). workspace-pinned.
UPDATE warmup_sends
SET status = 'sent', message_id = $3, sent_at = now(), last_error = ''
WHERE id = $1 AND workspace_id = $2 AND status = 'sending';

-- name: IncrementWarmupSentStat :exec
-- Bump the sender's daily sent counter, creating today's row on first send.
-- workspace_id is stamped on insert; the PK is (mailbox_id, day).
-- SAFETY: this is the one bare-VALUES insert in the send path (not the
-- self-enforcing INSERT ... SELECT FROM mailbox pattern the others use). It is
-- SAFE ONLY because it runs inside MarkWarmupSent's transaction AFTER the
-- workspace-pinned, self-enforcing SetWarmupSendSent claim has already proven the
-- (mailbox, workspace) pairing. A future refactor that calls this OUTSIDE that
-- gate MUST add the INSERT ... SELECT self-enforcement (like InsertWarmupThread).
INSERT INTO warmup_daily_stats (mailbox_id, workspace_id, day, sent)
VALUES ($1, $2, CURRENT_DATE, 1)
ON CONFLICT (mailbox_id, day) DO UPDATE SET sent = warmup_daily_stats.sent + 1;

-- name: ReleaseWarmupSend :exec
-- Release a claimed-but-unsent row after a RETRYABLE failure: back to 'queued'
-- with the lease cleared, so the asynq retry's ClaimWarmupSend reclaims it
-- immediately (the 'queued' reclaim branch) without waiting out the lease window.
-- Only touches a row still in 'sending'. workspace-pinned.
UPDATE warmup_sends
SET status = 'queued', claimed_at = NULL
WHERE id = $1 AND workspace_id = $2 AND status = 'sending';

-- name: FailWarmupSend :exec
-- Finalize a claimed row to 'failed' after a PERMANENT failure (no thread advance,
-- no stat bump). Only touches a row still in 'sending'. workspace-pinned.
UPDATE warmup_sends
SET status = 'failed', last_error = $3, claimed_at = NULL
WHERE id = $1 AND workspace_id = $2 AND status = 'sending';

-- ============================================================================
-- Receipt + engagement + health path (spec §4/§8) — the recipient-side seam.
-- Every statement is workspace_id-pinned; the receipt INSERT is SELF-ENFORCING
-- (INSERT ... SELECT FROM mailboxes WHERE id=<recipient> AND workspace_id=<ws>)
-- so a foreign (recipient, workspace) pair writes zero rows.
-- ============================================================================

-- name: UpsertWarmupReceipt :one
-- Idempotently record a received warmup message's placement. UNIQUE
-- (warmup_send_id, recipient_mailbox) makes a re-poll a no-op: ON CONFLICT DO
-- NOTHING, and RETURNING yields a row ONLY on a genuinely NEW insert (a duplicate
-- returns pgx.ErrNoRows). The INSERT proves all three identities at once: the send
-- belongs to the workspace, the recipient belongs to that workspace, and the send
-- was actually addressed to that recipient. A foreign recipient or a same-workspace
-- binding mismatch therefore also returns no row; the caller distinguishes a true
-- duplicate with GetWarmupReceiptByPair and otherwise fails closed or records
-- untrusted mismatch evidence. received_at
-- is returned so the caller seeds the deterministic engage plan on the SAME instant
-- a later GetWarmupEngageJob re-reads. source_folder + message_id are the receipt
-- locator (000019): the provider folder the message was found in and its RFC822
-- Message-ID, so C5b's engager can relocate/rescue/mark-read the exact message.
INSERT INTO warmup_receipts (workspace_id, warmup_send_id, recipient_mailbox, placement, source_folder, message_id)
SELECT s.workspace_id, s.id, m.id, @placement, @source_folder, @message_id
FROM warmup_sends s
JOIN mailboxes m ON m.id = @recipient_mailbox AND m.workspace_id = s.workspace_id
WHERE s.id = @warmup_send_id AND s.workspace_id = @workspace_id
  AND s.to_mailbox = @recipient_mailbox AND s.status = 'sent'
ON CONFLICT (warmup_send_id, recipient_mailbox) DO NOTHING
RETURNING id, received_at;

-- name: RecordWarmupPlacementObservation :exec
-- Immutable counterpart of the daily placement projection. Runs in the same
-- transaction as a newly inserted receipt; the receipt id is the idempotency key.
INSERT INTO warmup_observations (
    workspace_id, mailbox_id, observer_mailbox_id, warmup_send_id,
    kind, placement, source, attribution_trusted, idempotency_key, observed_at
)
SELECT s.workspace_id, s.from_mailbox, sqlc.arg(recipient_mailbox)::uuid, s.id,
       'placement', sqlc.arg(placement)::text, 'warmup_receipt', true,
       'receipt:' || sqlc.arg(receipt_id)::uuid::text, sqlc.arg(observed_at)::timestamptz
FROM warmup_sends s
WHERE s.id = sqlc.arg(warmup_send_id)
  AND s.workspace_id = sqlc.arg(workspace_id)
  AND s.to_mailbox = sqlc.arg(recipient_mailbox)
ON CONFLICT (workspace_id, idempotency_key) DO NOTHING;

-- name: RecordWarmupTokenFailureObservation :exec
-- Untrusted token failures retain no claimed sender. The recipient mailbox is
-- ownership-checked, but attribution_trusted stays false and mailbox_id stays NULL
-- (both now CHECK-enforced by migration 000055) so this evidence can inform the
-- future observer-trust axis without health-gating an innocent sender: an
-- unauthenticated token may claim ANY sender, and trusting the claim would let
-- anyone throttle a mailbox they do not own by emailing it three times.
--
-- The idempotency key buckets on (mailbox, UTC date, reason_code), NOT on a hash of
-- the token. The token is an attacker-controlled header, so hashing it made every
-- distinct forged token a permanent row that anyone able to email a connected
-- mailbox could write — unbounded growth in an append-only table by external input
-- (design §4.6). Bucketing writes at most one row per mailbox per day per reason.
-- The trade is deliberate: individual forged tokens are no longer distinguishable
-- rows, so the caller LOGS each occurrence (poll.go / RecordWarmupReceipt) while the
-- table keeps only the bounded "this mailbox saw forged traffic that day" fact.
--
-- The date is computed in UTC, matching the UTC-day convention the daily-stats
-- reads and the snapshot windows use.
INSERT INTO warmup_observations (
    workspace_id, observer_mailbox_id, kind, source, reason_code,
    attribution_trusted, idempotency_key, observed_at
)
SELECT sqlc.arg(workspace_id), m.id, 'invalid_token', 'inbox_token_verifier',
       sqlc.arg(reason_code), false,
       'token:' || m.id::text || ':'
                || to_char(now() AT TIME ZONE 'utc', 'YYYY-MM-DD') || ':'
                || sqlc.arg(reason_code)::text,
       now()
FROM mailboxes m
WHERE m.id = sqlc.arg(recipient_mailbox)
  AND m.workspace_id = sqlc.arg(workspace_id)
ON CONFLICT (workspace_id, idempotency_key) DO NOTHING;

-- name: RecordWarmupHardBounceObservation :one
-- A DSN is matched by the provider-returned Message-ID on the warmup send. The
-- CTE returns matched=true even on an idempotent duplicate so the inbox poller
-- never falls through and misclassifies a warmup DSN as a campaign bounce.
--
-- The observer binding (s.from_mailbox = @observer_mailbox) is a SECURITY control,
-- not a filter. Original-Message-ID is parsed out of the inbound DSN body and is
-- therefore fully attacker-controlled; without this predicate a forged DSN
-- delivered to ANY connected mailbox in the workspace — a public sales@ alias, a
-- support inbox — would write a TRUSTED hard bounce attributed to a DIFFERENT
-- mailbox, and since Phase 1 that can quarantine the sender and fail its
-- campaign's preflight, not merely trim its capacity.
--
-- It costs no true positives: a DSN for a warmup send comes back to that send's
-- own Return-Path, so the polled mailbox IS the sender. The forgery surface
-- shrinks from "any connected mailbox" to "the sender's own mailbox", where an
-- attacker would already need the Message-ID, which is CSPRNG-generated and never
-- leaves the workspace.
WITH candidate AS (
    SELECT s.id, s.workspace_id, s.from_mailbox
    FROM warmup_sends s
    WHERE s.workspace_id = @workspace_id AND s.message_id = @message_id
      AND s.status = 'sent'
      AND s.from_mailbox = @observer_mailbox
    ORDER BY s.sent_at DESC
    LIMIT 1
), inserted AS (
    INSERT INTO warmup_observations (
        workspace_id, mailbox_id, observer_mailbox_id, warmup_send_id, kind, source,
        reason_code, attribution_trusted, idempotency_key, observed_at
    )
    -- observer_mailbox_id records WHERE the DSN arrived, as the placement path
    -- already does. Phase 0 left it NULL, so a mis-delivered DSN was unattributable
    -- after the fact.
    SELECT c.workspace_id, c.from_mailbox, @observer_mailbox, c.id, 'hard_bounce',
           'inbox_dsn', 'hard_bounce', true, 'bounce:' || c.id::text, now()
    FROM candidate c
    ON CONFLICT (workspace_id, idempotency_key) DO NOTHING
    RETURNING 1
)
SELECT EXISTS(SELECT 1 FROM candidate) AS matched,
       EXISTS(SELECT 1 FROM inserted) AS inserted;

-- name: ReclassifyWarmupReceiptToSpam :one
-- A message seen in the INBOX and later found in junk. First-observation-wins is
-- right for idempotency but wrong for a reputation signal: spam-after-inbox is the
-- single most important placement change there is, and the receipt upsert
-- short-circuits before the observation write, so it used to be discarded.
--
-- The observation row is SUPERSEDED IN PLACE rather than joined by a second row.
-- The idempotency key IS the receipt id, so a second row would need a different
-- key — and the snapshot aggregation counts placement rows, so the same message
-- would then be counted twice: once as inbox and once as spam, inflating the spam
-- numerator, the placement denominator and the minimum-sample gate together.
-- Deduplicating in the aggregation instead would mean grouping on a receipt id the
-- observations table does not carry as a column. One row per message keeps
-- "placement samples" meaning messages, which is what the thresholds are calibrated
-- against.
--
-- MONOTONE, and that direction is load-bearing rather than defensive: the engager
-- deliberately RESCUES spam messages into the inbox, so a later poll legitimately
-- finds them there. Allowing spam -> inbox would let our own rescue erase the
-- evidence that the rescue was needed. `placement <> 'spam'` is also what makes
-- this exactly-once: a re-poll of an already-reclassified receipt matches no row,
-- so nothing is written and no counter moves twice. Two concurrent pollers race on
-- the receipt row lock and the loser re-evaluates the predicate against the
-- winner's committed value, matching nothing.
--
-- observed_at advances to now() ONLY for a receipt still inside the placement
-- window. The message being in spam now is genuinely current information, and
-- filing it at the original observation time would leave a current fact outside
-- the window that decides whether the participant is measured at all.
--
-- But it must not RESURRECT aged evidence. FetchJunk is deliberately stateless and
-- rescans the newest ~100 junk messages every poll, and a message moved into Junk
-- acquires a fresh high UID there — so without this bound, anyone with IMAP access
-- to a RECIPIENT warmup mailbox (an in-workspace colleague, a shared inbox) could
-- bulk-move 90 days of warmup history into Junk and mint up to 100 spam samples
-- dated now, per poll, until the SENDER paused and its whole domain was withheld.
-- Bounding the timestamp keeps the correction honest about WHAT without letting it
-- rewrite WHEN: an aged message still has its placement and its daily counter
-- corrected, it simply cannot re-enter the window that gates health.
--
-- The daily projection is corrected on the day the message was RECEIVED, which is
-- where RecordWarmupSenderPlacementStat put the original count, and only the inbox
-- counter it actually incremented is decremented ('other' incremented neither).
-- Otherwise the overview and the deliverability score would keep reporting an inbox
-- placement the policy has already recorded as spam.
--
-- `prior` is a second reference to the same row, read from the statement snapshot,
-- which is how the pre-update placement is recovered: RETURNING would give the new
-- value.
WITH promoted AS (
    UPDATE warmup_receipts r
    SET placement = 'spam'
    FROM warmup_receipts prior
    JOIN warmup_sends s ON s.id = prior.warmup_send_id AND s.workspace_id = prior.workspace_id
    WHERE prior.id = r.id
      AND r.workspace_id = @workspace_id
      AND r.warmup_send_id = @warmup_send_id
      AND r.recipient_mailbox = @recipient_mailbox
      AND r.placement <> 'spam'
    RETURNING r.id, r.received_at, prior.placement AS prior_placement, s.from_mailbox
), observation AS (
    UPDATE warmup_observations o
    SET placement = 'spam',
        observed_at = CASE
            WHEN p.received_at >= now() - interval '7 days' THEN now()
            ELSE o.observed_at
        END
    FROM promoted p
    WHERE o.workspace_id = @workspace_id
      AND o.idempotency_key = 'receipt:' || p.id::text
      AND o.kind = 'placement'
    RETURNING o.id
), projection AS (
    UPDATE warmup_daily_stats d
    SET inbox = greatest(d.inbox - CASE WHEN p.prior_placement = 'inbox' THEN 1 ELSE 0 END, 0),
        spam  = d.spam + 1
    FROM promoted p
    WHERE d.mailbox_id = p.from_mailbox
      AND d.workspace_id = @workspace_id
      AND d.day = (p.received_at AT TIME ZONE 'utc')::date
    RETURNING d.mailbox_id
)
SELECT EXISTS(SELECT 1 FROM promoted) AS reclassified,
       EXISTS(SELECT 1 FROM observation) AS observation_superseded;

-- name: GetWarmupReceiptByPair :one
-- Disambiguates an UpsertWarmupReceipt that inserted zero rows: a workspace-pinned
-- lookup on the same (send, recipient) pair. A hit means a genuine DUPLICATE (same
-- workspace, already recorded → idempotent no-op); a miss means the recipient does
-- not belong to the workspace (the self-enforcing INSERT's SELECT was empty →
-- cross-tenant). workspace-pinned. Deliberately a PURE receipt read (no participant
-- join) so the hit/miss semantics stay exactly duplicate-vs-cross-tenant. engaged,
-- received_at and placement are returned so the caller can, on an UNENGAGED
-- duplicate, rebuild the SAME deterministic engage plan the fresh insert produced and
-- re-enqueue it — self-healing an engagement lost to a post-commit enqueue failure.
-- The recipient's reply_rate is read separately (GetWarmupParticipant) to keep this a
-- single-table read.
SELECT id, engaged, received_at, placement FROM warmup_receipts
WHERE warmup_send_id = $1 AND recipient_mailbox = $2 AND workspace_id = $3;

-- name: RecordWarmupReceivedStat :exec
-- On a NEWLY inserted receipt, bump the RECIPIENT's daily received counter for the
-- UTC day (CURRENT_DATE, matching the C1 UTC-day convention), creating today's row
-- on first receipt. This is a recipient-side VOLUME counter only ("how much warmup
-- mail did I receive") — it is NOT a reputation signal. inbox/spam PLACEMENT is a
-- sender-deliverability signal and is attributed to the SENDER separately
-- (RecordWarmupSenderPlacementStat), because deliverability belongs to whoever SENT
-- the mail, not whoever observed where it landed. Attributing spam to the recipient
-- would invert the signal (punish the innocent inbox owner, never flag the sender).
-- SAFETY: a bare-VALUES insert (like IncrementWarmupSentStat), SAFE ONLY because it
-- runs inside RecordWarmupReceipt's transaction AFTER the workspace-pinned,
-- self-enforcing UpsertWarmupReceipt has already proven the (recipient, workspace)
-- pairing. A future caller OUTSIDE that gate MUST add INSERT ... SELECT
-- self-enforcement.
INSERT INTO warmup_daily_stats (mailbox_id, workspace_id, day, received)
VALUES ($1, $2, CURRENT_DATE, 1)
ON CONFLICT (mailbox_id, day) DO UPDATE SET
    received = warmup_daily_stats.received + 1;

-- name: RecordWarmupSenderPlacementStat :exec
-- On a NEWLY inserted receipt, bump the SENDER's daily inbox|spam placement counter
-- for the UTC day. The sender is resolved from warmup_sends.from_mailbox for this
-- warmup_send_id, because inbox-vs-spam placement is a SENDER-deliverability signal
-- ("did MY outbound warmup mail land in the inbox or spam at partners?"). The
-- recipient merely OBSERVES the placement. 'other' placement increments neither
-- counter. SELF-ENFORCING tenancy: the INSERT ... SELECT emits a row ONLY when the
-- send truly belongs to the workspace, so a foreign (send, workspace) pair inserts
-- zero rows; the resolved (sender, workspace) pairing is proven by the same join.
INSERT INTO warmup_daily_stats (mailbox_id, workspace_id, day, inbox, spam)
SELECT s.from_mailbox, s.workspace_id, CURRENT_DATE,
       CASE WHEN sqlc.arg(placement)::text = 'inbox' THEN 1 ELSE 0 END,
       CASE WHEN sqlc.arg(placement)::text = 'spam'  THEN 1 ELSE 0 END
FROM warmup_sends s
WHERE s.id = sqlc.arg(warmup_send_id) AND s.workspace_id = sqlc.arg(workspace_id)
ON CONFLICT (mailbox_id, day) DO UPDATE SET
    inbox = warmup_daily_stats.inbox + EXCLUDED.inbox,
    spam  = warmup_daily_stats.spam + EXCLUDED.spam;

-- name: GetWarmupEngageBundle :one
-- Everything GetWarmupEngageJob needs that is always present: the recipient's
-- send transport (SMTP, for the reply) AND its IMAP-MODIFY transport (for
-- mark-read/rescue), both decrypted at the caller from the one secret_ciphertext;
-- the receipt's source_folder + message_id (the engager locates/rescues the exact
-- message by them); its participant reply_rate (to recompute the deterministic reply
-- decision); the placement (rescue decision); and received_at (seed anchor). INNER
-- joins keep every column non-null; the two joins are also workspace-pinned
-- (belt-and-braces). warmup_send_id is carried through so the caller can derive the
-- reply's receipt token. A foreign / vanished receipt yields pgx.ErrNoRows.
SELECT r.recipient_mailbox, r.warmup_send_id, r.placement, r.received_at,
       -- Same reasoning as GetWarmupSenderBundle: the reply is a NEW warmup send
       -- and needs its own lease, minted on the database clock.
       (now() + make_interval(secs => sqlc.arg(lease_seconds)::int))::timestamptz AS lease_expires_at,
       r.source_folder, r.message_id,
       m.provider, m.imap_host, m.imap_port, m.imap_username,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext,
       m.allow_plaintext, p.reply_rate, p.lane
FROM warmup_receipts r
JOIN mailboxes m ON m.id = r.recipient_mailbox AND m.workspace_id = r.workspace_id
JOIN warmup_participants p ON p.mailbox_id = r.recipient_mailbox AND p.workspace_id = r.workspace_id
WHERE r.id = $1 AND r.workspace_id = $2;

-- name: GetWarmupReplyThread :one
-- The thread + addressing behind a receipt, for building a reply turn that is a NEW
-- warmup send FROM the recipient BACK TO the original sender. INNER joins through the
-- receipt's warmup_send_id → send → thread, so a receipt whose send was deleted
-- (warmup_send_id SET NULL) or whose thread vanished yields pgx.ErrNoRows and the
-- caller simply builds no reply. sender_mailbox/sender_email are the ORIGINAL sender
-- (warmup_sends.from_mailbox — the reply's To); recipient_email/recipient_name are
-- the replier's own envelope (the reply's From). Both mailbox joins are
-- workspace-pinned (belt-and-braces), like the receipt WHERE.
--
-- sender_lane is the ORIGINAL sender's CURRENT pool lane. Partner selection proved
-- the two were lane-compatible when the outbound send was made, but a reply lands
-- minutes to hours later (and a receipt can be re-engaged later still), by which
-- time either side may have moved. Without re-checking, a mailbox that has since
-- been quarantined keeps emitting warmup mail, and a healthy peer keeps receiving
-- from a peer that has left the healthy pool.
SELECT COALESCE(sp.lane, '')::text AS sender_lane,
       t.id AS thread_id, t.turn, t.content_key, t.root_message_id,
       s.from_mailbox AS sender_mailbox,
       sm.email AS sender_email,
       rm.email AS recipient_email,
       rm.display_name AS recipient_name
FROM warmup_receipts r
JOIN warmup_sends s ON s.id = r.warmup_send_id
JOIN warmup_threads t ON t.id = s.thread_id
JOIN mailboxes sm ON sm.id = s.from_mailbox AND sm.workspace_id = r.workspace_id
JOIN mailboxes rm ON rm.id = r.recipient_mailbox AND rm.workspace_id = r.workspace_id
LEFT JOIN warmup_participants sp
       ON sp.mailbox_id = s.from_mailbox AND sp.workspace_id = r.workspace_id
WHERE r.id = $1 AND r.workspace_id = $2;

-- name: SetWarmupReceiptEngaged :one
-- Mark a receipt engaged so a retried engage is a no-op. Guarded on NOT engaged and
-- RETURNING recipient_mailbox: this call flips the flag (and RETURNS the recipient
-- so the caller can bump its reply counter) ONLY the first time; a re-run over an
-- already-engaged row affects zero rows and RETURNS pgx.ErrNoRows (idempotent).
-- workspace-pinned.
UPDATE warmup_receipts
SET engaged = true
WHERE id = $1 AND workspace_id = $2 AND NOT engaged
RETURNING recipient_mailbox;

-- name: IncrementWarmupReplyStat :exec
-- Bump the RECIPIENT's daily replies counter when an engagement replied, creating
-- today's row on first reply. workspace_id is stamped on insert; PK is
-- (mailbox_id, day).
-- SAFETY: bare-VALUES insert, SAFE ONLY inside MarkWarmupEngaged's transaction
-- AFTER the workspace-pinned SetWarmupReceiptEngaged has returned the recipient it
-- proved belongs to the workspace-pinned receipt (same gate as IncrementWarmupSentStat).
INSERT INTO warmup_daily_stats (mailbox_id, workspace_id, day, replies)
VALUES ($1, $2, CURRENT_DATE, 1)
ON CONFLICT (mailbox_id, day) DO UPDATE SET replies = warmup_daily_stats.replies + 1;

-- name: ListDueWarmupMailboxes :many
-- (mailbox, workspace) for every enabled, non-paused participant in a lane that
-- MAY send — the sweep fan-out. Deliberately coarse otherwise: precise ramp/window
-- due-gating is delegated to NextWarmupDue in the send handler (C4), so this only
-- filters out what can never send now. The sealed lanes are part of that: a
-- pending_auth, quarantined or blocked participant has no eligible partner by
-- construction (same-lane pairing above), so ticking it could only ever produce a
-- Skip. A lone same-lane participant is still returned; its GetWarmupSendJob then
-- Skips for want of a partner. Global fan-out (no workspace pin), like
-- ListActiveMailboxes.
SELECT mailbox_id, workspace_id FROM warmup_participants
WHERE enabled
  AND health_state <> 'paused'
  AND (paused_until IS NULL OR paused_until <= now())
  AND lane NOT IN ('pending_auth','quarantine','blocked')
ORDER BY mailbox_id;

-- ============================================================================
-- Phase 1 reputation network (docs/superpowers/specs/
-- 2026-08-12-warmup-reputation-phase-1-design.md). Evidence is MATERIALIZED once
-- per workspace per sweep instead of recomputed per participant: Phase 0 re-ran
-- eight correlated subqueries for EVERY enabled participant on every five-minute
-- tick, one of them an arm over sequence_enrollments no index could serve. The
-- sweep now issues a bounded number of statements per WORKSPACE (design §3.1,
-- acceptance criterion 7).
-- ============================================================================

-- name: ListWorkspacesWithWarmupParticipants :many
-- Drives the per-workspace snapshot loop. Global fan-out (no workspace pin), like
-- ListDueWarmupMailboxes: the sweep is infrastructure maintenance rather than a
-- tenant read, and every statement it then issues is pinned to one of these ids.
SELECT DISTINCT workspace_id FROM warmup_participants
WHERE enabled
ORDER BY workspace_id;

-- name: UpsertWarmupSignalSnapshotsForWorkspace :execrows
-- Recompute every enabled participant's evidence for ONE workspace in ONE
-- statement. Each population keeps its OWN denominator; unlike populations are
-- never summed (design §4.1). Phase 0 pooled campaign and warmup sends into one
-- bounce denominator, and warmup traffic — synthetic mail between the operator's
-- own mailboxes, which essentially never hard-bounces — diluted it below the
-- thresholds it was meant to trip: 20 hard bounces on 200 campaign sends is a 10%
-- rate, but 20/(200+1200) reads as 1.4%, under even the watch band. Worse, warmup
-- volume ALONE cleared the minimum-sample gate, so the evidence gate opened on
-- data containing no bounce information at all.
--
-- Windows: placement over 7 days (the qualified clean window), delivered/bounce/
-- complaint populations over 30 days, observer token failures over 7 days.
-- $1 pins the workspace on the outer WHERE and on every subquery.
INSERT INTO warmup_signal_snapshots (
    workspace_id, mailbox_id, computed_at,
    placement_inbox, placement_spam,
    campaign_delivered, campaign_hard_bounces, campaign_asserted_hard_bounces, campaign_complaints,
    warmup_delivered, warmup_hard_bounces,
    observer_token_failures, newest_evidence_at
)
SELECT p.workspace_id, p.mailbox_id, now(),
       COALESCE(place.inbox, 0)::int,
       COALESCE(place.spam, 0)::int,
       COALESCE(camp.delivered, 0)::int,
       COALESCE(camp.hard_bounces, 0)::int,
       COALESCE(camp.asserted_hard_bounces, 0)::int,
       COALESCE(camp.complaints, 0)::int,
       COALESCE(warm.delivered, 0)::int,
       COALESCE(warm.hard_bounces, 0)::int,
       COALESCE(tokens.failures, 0)::int,
       evidence.newest_at
FROM warmup_participants p
LEFT JOIN LATERAL (
    -- Placement is SENDER-attributed (security invariant 29): a warmup message
    -- landing in spam degrades whoever SENT it, not the mailbox that observed it.
    SELECT count(*) FILTER (WHERE o.placement = 'inbox') AS inbox,
           count(*) FILTER (WHERE o.placement = 'spam') AS spam
    FROM warmup_observations o
    WHERE o.workspace_id = $1
      AND o.mailbox_id = p.mailbox_id
      AND o.kind = 'placement'
      AND o.attribution_trusted
      AND o.observed_at >= now() - interval '7 days'
) place ON true
LEFT JOIN LATERAL (
    SELECT
        (
            SELECT count(*)
            FROM sends s
            WHERE s.workspace_id = $1
              AND s.mailbox_id = p.mailbox_id
              AND s.status = 'sent'
              AND s.sent_at >= now() - interval '30 days'
        ) AS delivered,
        (
            -- Only the deliverability_events arm, and only bounce_class='hard'.
            --
            -- The Phase 0 enrollment arm joined sequence_enrollments to sends on
            -- (workspace_id, campaign_id, contact_id) with NO sender identity: a
            -- campaign rotating over mailboxes M and N, where contact X bounced on
            -- N's step, has a sends row for (C, X) under BOTH — so the bounce was
            -- counted against M as well, throttling a clean mailbox for another
            -- mailbox's failure, with the error scaling with pool rotation. This
            -- arm carries send_id and therefore resolves the ACTUAL sender
            -- (design §4.3). The enrollment arm stays correct at CAMPAIGN scope
            -- and is left alone there.
            --
            -- bounce_class filters out soft bounces. Provider feeds include full
            -- mailbox and greylisting (security invariant 42), and Phase 0 fed
            -- them into a rate it reported as "hard-bounce rate above 10%", so a
            -- normal week of greylisting could pause a healthy mailbox for 72h.
            -- Rows predating the column are 'unknown' and excluded: under-counting
            -- history is the safe direction.
            --
            -- DISTINCT s.id counts bounced SENDS, matching the delivered-sends
            -- denominator above. Two DSNs for one send would otherwise be able to
            -- push the numerator past the denominator.
            -- TWO arms, unioned on the send so a bounce counted by both is counted
            -- once. Neither alone is sufficient:
            --
            -- (a) provider webhooks carry bounce_class, populated at ingest from
            --     the DeliverabilityEvent body and validated there against these
            --     same three values. Rows written before the column existed, and
            --     any feed that does not classify, are 'unknown' and stay out of
            --     the numerator.
            -- (b) Inroad's OWN DSN parser already distinguishes hard bounces and
            --     stops the enrollment with stop_reason='bounced'. Without this arm
            --     the whole campaign hard-bounce guardrail is structurally zero —
            --     the same "rule fed by a permanently-empty counter" defect this
            --     phase set out to remove, reintroduced on the bounce axis.
            --
            -- (b) is attributed through the LAST send before the enrollment stopped,
            -- which is the send that bounced. Phase 0 joined on
            -- (campaign_id, contact_id) with no sender identity, so a campaign
            -- rotating over M and N charged BOTH for one bounce.
            -- SELF-OBSERVED only: the enrollment stopped as bounced because
            -- Inroad's own DSN parser classified it. This arm can contain a
            -- mailbox. Attributed through the LAST send before the enrollment
            -- stopped, which is the send that bounced.
            SELECT count(*) FROM (
                SELECT bounced.id
                FROM sequence_enrollments se
                JOIN LATERAL (
                    SELECT s.id, s.mailbox_id
                    FROM sends s
                    WHERE s.workspace_id = se.workspace_id
                      AND s.campaign_id = se.campaign_id
                      AND s.contact_id = se.contact_id
                      AND s.status = 'sent'
                      AND s.sent_at <= se.stopped_at
                      AND s.sent_at >= now() - interval '30 days'
                    ORDER BY s.sent_at DESC
                    LIMIT 1
                ) bounced ON true
                WHERE se.workspace_id = $1
                  AND se.stop_reason = 'bounced'
                  AND se.stopped_at >= now() - interval '30 days'
                  AND bounced.mailbox_id = p.mailbox_id
            ) hard_bounced_sends
        ) AS hard_bounces,
        (
            -- ASSERTED by whoever holds deliverability:write. Real evidence, but
            -- not self-observed, so the policy caps what it can do (see
            -- assertedBand in policy.go). Same window and status bound as the
            -- denominator: a late DSN for an older send must not count against a
            -- denominator that excludes it.
            SELECT count(DISTINCT s.id)
            FROM deliverability_events de
            JOIN sends s ON s.id = de.send_id AND s.workspace_id = de.workspace_id
            WHERE de.workspace_id = $1
              AND s.mailbox_id = p.mailbox_id
              AND de.kind = 'bounce'
              AND de.bounce_class = 'hard'
              AND de.received_at >= now() - interval '30 days'
              AND s.status = 'sent'
              AND s.sent_at >= now() - interval '30 days'
        ) AS asserted_hard_bounces,
        (
            SELECT count(DISTINCT s.id)
            FROM deliverability_events de
            JOIN sends s ON s.id = de.send_id AND s.workspace_id = de.workspace_id
            WHERE de.workspace_id = $1
              AND s.mailbox_id = p.mailbox_id
              AND de.kind = 'complaint'
              AND de.received_at >= now() - interval '30 days'
              AND s.status = 'sent'
              AND s.sent_at >= now() - interval '30 days'
        ) AS complaints
) camp ON true
LEFT JOIN LATERAL (
    SELECT
        (
            SELECT count(*)
            FROM warmup_sends s
            WHERE s.workspace_id = $1
              AND s.from_mailbox = p.mailbox_id
              AND s.status = 'sent'
              AND s.sent_at >= now() - interval '30 days'
        ) AS delivered,
        (
            SELECT count(*)
            FROM warmup_observations o
            WHERE o.workspace_id = $1
              AND o.mailbox_id = p.mailbox_id
              AND o.kind = 'hard_bounce'
              AND o.attribution_trusted
              AND o.observed_at >= now() - interval '30 days'
        ) AS hard_bounces
) warm ON true
LEFT JOIN LATERAL (
    -- OBSERVER-side, matched on observer_mailbox_id: "this mailbox is receiving
    -- forged warmup traffic". Phase 0 read it on mailbox_id with an
    -- attribution_trusted predicate, but the writer records invalid tokens with
    -- mailbox_id NULL and attribution_trusted false ON PURPOSE — an unauthenticated
    -- token may claim any sender, so trusting the claim would let anyone throttle a
    -- mailbox they do not own by emailing it three times. Both requirements were
    -- therefore structurally unsatisfiable and the counter was always zero, which
    -- is why migration 000055 turned that safeguard into two CHECK constraints
    -- (design §4.5). attribution_trusted describes the DISCARDED sender claim, not
    -- the observation, so it has no business filtering an observer-side count.
    --
    -- Nothing automatic acts on this number in Phase 1: it is operator visibility
    -- and the seed of a future observer-trust axis. The 7-day window pairs with
    -- the per-mailbox-per-day-per-reason idempotency key
    -- (RecordWarmupTokenFailureObservation), so the count reads as "days this week
    -- on which forged traffic arrived, per reason" and cannot be inflated without
    -- bound by an external sender.
    SELECT count(*) AS failures
    FROM warmup_observations o
    WHERE o.workspace_id = $1
      AND o.observer_mailbox_id = p.mailbox_id
      AND o.kind = 'invalid_token'
      AND o.observed_at >= now() - interval '7 days'
) tokens ON true
LEFT JOIN LATERAL (
    -- How old the newest evidence about THIS mailbox's own mail is. It drives the
    -- freshness rule in ListWarmupEvaluationRows, so its definition is
    -- load-bearing rather than informational.
    --
    -- SUBJECT-side and TRUSTED only, deliberately. The observer-side arm this
    -- replaces counted invalid_token rows, which an external sender can cause by
    -- emailing a connected mailbox — so anyone could have kept a mailbox's
    -- evidence looking "fresh" without a single observation about its outbound
    -- mail. attribution_trusted is the same gate the rate arms above use, and the
    -- migration 000055 CHECK guarantees invalid_token rows can never set it.
    -- NULL (no evidence at all) is preserved and must read as NOT fresh.
    --
    -- Range-seeks idx_warmup_observations_subject_time on its
    -- (workspace_id, mailbox_id) prefix.
    SELECT max(o.observed_at) AS newest_at
    FROM warmup_observations o
    WHERE o.workspace_id = $1
      AND o.mailbox_id = p.mailbox_id
      AND o.attribution_trusted
) evidence ON true
WHERE p.workspace_id = $1 AND p.enabled
ON CONFLICT (workspace_id, mailbox_id) DO UPDATE SET
    computed_at             = EXCLUDED.computed_at,
    placement_inbox         = EXCLUDED.placement_inbox,
    placement_spam          = EXCLUDED.placement_spam,
    campaign_delivered      = EXCLUDED.campaign_delivered,
    campaign_hard_bounces   = EXCLUDED.campaign_hard_bounces,
    campaign_asserted_hard_bounces = EXCLUDED.campaign_asserted_hard_bounces,
    campaign_complaints     = EXCLUDED.campaign_complaints,
    warmup_delivered        = EXCLUDED.warmup_delivered,
    warmup_hard_bounces     = EXCLUDED.warmup_hard_bounces,
    observer_token_failures = EXCLUDED.observer_token_failures,
    newest_evidence_at      = EXCLUDED.newest_evidence_at;

-- name: ListWarmupEvaluationRows :many
-- One workspace's enabled participants, each with both current axes and the
-- materialized evidence the policy reads. Pinned on $1.
--
-- The snapshot join is LEFT on purpose: a participant enabled between the refresh
-- and this read has no snapshot row, and NULL computed_at must read as "no
-- evidence" (unknown, no promotion) rather than as zeros that look clean. Absence
-- of evidence is never health — that is the whole point of Phase 0's unknown state
-- and this phase's staleness rule (design §8, acceptance criterion 3).
--
-- auth_passing is the admission prerequisite (design §6): the mailbox's
-- organizational domain resolved to 'passing' AND the mailbox is connected (not in
-- credential error). It deliberately does NOT consider dkim_found — migration
-- 000036 documents DKIM as advisory because selectors are not discoverable from
-- DNS, so dkim_found=false means "none of the probed selectors matched", not
-- "unsigned", and gating on it would strand correctly-signed domains in
-- pending_auth forever. 'unknown' (resolver timeout — could not check) does not
-- open the gate either; it waits for the domainauth sweep.
--
-- quarantined_since is derived from the transition trail rather than stored on the
-- participant: the newest row that MOVED it into quarantine. It gates the cooldown
-- only — elapsing is necessary but never sufficient (acceptance criterion 2).
SELECT p.mailbox_id,
       p.workspace_id,
       p.health_state,
       p.lane,
       p.paused_until,
       (COALESCE(d.state, '') = 'passing' AND m.status = 'active')::boolean AS auth_passing,
       -- Freshness measures the age of the EVIDENCE, not the age of the snapshot.
       --
       -- computed_at is written by the upsert that runs immediately before this
       -- read, in the same sweep, so a computed_at-based test was true for every
       -- participant on every tick except one enabled between the two statements:
       -- a staleness rule that could not detect staleness. newest_evidence_at is
       -- the column that actually ages, and NULL — no evidence at all — reads as
       -- NOT fresh, which is the direction acceptance criterion 3 requires
       -- (absence of evidence is never health).
       --
       -- Still decided by the DATABASE clock on both sides. Comparing a
       -- Go-injected clock against a DB-generated timestamp makes any app/DB skew
       -- look like stale evidence, which fails CLOSED — quiet, and hard to
       -- diagnose. The TTL keeps one home: the caller passes it in seconds.
       (s.newest_evidence_at IS NOT NULL
        AND s.newest_evidence_at >= now() - make_interval(secs => sqlc.arg(evidence_ttl_seconds)::int)) AS evidence_fresh,
       COALESCE(s.placement_inbox, 0)::int         AS placement_inbox,
       COALESCE(s.placement_spam, 0)::int          AS placement_spam,
       COALESCE(s.campaign_delivered, 0)::int      AS campaign_delivered,
       COALESCE(s.campaign_hard_bounces, 0)::int   AS campaign_hard_bounces,
       COALESCE(s.campaign_asserted_hard_bounces, 0)::int AS campaign_asserted_hard_bounces,
       COALESCE(s.campaign_complaints, 0)::int     AS campaign_complaints,
       COALESCE(s.warmup_delivered, 0)::int        AS warmup_delivered,
       COALESCE(s.warmup_hard_bounces, 0)::int     AS warmup_hard_bounces,
       COALESCE(s.observer_token_failures, 0)::int AS observer_token_failures,
       q.quarantined_since,
       lapse.evidence_lapsed_since
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id AND m.workspace_id = p.workspace_id
LEFT JOIN warmup_signal_snapshots s
       ON s.mailbox_id = p.mailbox_id AND s.workspace_id = p.workspace_id
LEFT JOIN sending_domains d
       ON d.workspace_id = p.workspace_id
      AND d.domain = lower(split_part(m.email, '@', 2))
LEFT JOIN LATERAL (
    SELECT max(t.created_at)::timestamptz AS quarantined_since
    FROM warmup_state_transitions t
    WHERE t.workspace_id = p.workspace_id
      AND t.mailbox_id = p.mailbox_id
      AND t.to_lane = 'quarantine'
      -- Only rows that MOVED it into quarantine start the clock. Health-only
      -- transitions written while already quarantined carry
      -- from_lane = to_lane = 'quarantine', and counting those restarted the
      -- cooldown on every sweep the mailbox made recovery progress — so improving
      -- extended the containment that improvement was supposed to end.
      AND t.from_lane IS DISTINCT FROM 'quarantine'
) q ON true
LEFT JOIN LATERAL (
    -- When the health axis last FELL to unknown — the moment this participant
    -- stopped having qualified evidence. It anchors the healthy lane's grace
    -- period (design §6 hysteresis): a lapse has to persist before it costs a
    -- mailbox its lane, while a degraded signal still contains it on the first
    -- sweep.
    --
    -- Derived from the transition trail rather than stored on the participant,
    -- for the same reason quarantined_since is: the trail is append-only and
    -- already records exactly this event, so there is no second source of truth
    -- to drift.
    --
    -- from_state <> 'unknown' keeps it the moment of the FALL. Rows written while
    -- already unknown (a lane move with no health change) carry
    -- from_state = to_state = 'unknown', and counting those would restart the
    -- grace every time anything else happened — the same defect the quarantine
    -- cooldown had.
    SELECT max(t.created_at)::timestamptz AS evidence_lapsed_since
    FROM warmup_state_transitions t
    WHERE t.workspace_id = p.workspace_id
      AND t.mailbox_id = p.mailbox_id
      AND t.to_state = 'unknown'
      AND t.from_state <> 'unknown'
) lapse ON true
WHERE p.workspace_id = $1 AND p.enabled
ORDER BY p.mailbox_id;

-- name: PurgeWarmupObservations :one
-- Bound the evidence table (design §4.6). warmup_observations is append-only and
-- was in no maintenance sweep, while its invalid-token rows are written on behalf
-- of anyone who can email a connected mailbox. 90 days is comfortably beyond the
-- widest read window above (30), so retention can never remove evidence the policy
-- would still have acted on. Batched at 5000 rows like every other purge in
-- queries/maintenance.sql, to cap one sweep's lock/IO footprint.
--
-- Global (no workspace pin), for the same reason PurgeExpiredSecurityArtifacts is:
-- retention is deployment maintenance, not a tenant read. It removes rows by age
-- alone and returns only a count, so it can neither surface nor cross tenant data.
WITH deleted AS (
    DELETE FROM warmup_observations
    WHERE id IN (
        SELECT id FROM warmup_observations
        WHERE observed_at < now() - interval '90 days'
        ORDER BY observed_at LIMIT 5000
    )
    RETURNING 1
)
SELECT count(*)::bigint AS deleted_rows FROM deleted;

-- name: UpdateWarmupHealth :exec
-- Persist a health transition for one participant: new state, human-readable
-- reason, and the pause window (NULL clears it on recovery to watch/healthy).
-- workspace-pinned.
UPDATE warmup_participants
SET health_state = $3, health_reason = $4, paused_until = $5, updated_at = now()
WHERE mailbox_id = $1 AND workspace_id = $2;

-- name: ApplyWarmupParticipantTransition :one
-- BOTH axes and their explanations are ONE atomic statement, so "quarantined but
-- healthy" is unrepresentable and no applied transition can lose its audit trail.
--
-- The compare-and-set guards BOTH from_state AND from_lane: two evaluators racing
-- on the same participant would otherwise each read one axis, and the loser would
-- overwrite the winner's decision on the other — writing history that never
-- happened. A guard miss means another evaluator already moved it, and the caller
-- simply skips (applied=false) rather than retrying with a stale decision.
--
-- Each axis carries its own reason_code/reason. One slot cannot serve two
-- independent decisions: when health says "spam placement above the pause
-- threshold" and the lane says "quarantined", collapsing them destroys whichever
-- loses.
--
-- bounce_population/bounce_samples/bounce_rate hold the arm that ACTUALLY drove
-- the decision — the higher of the campaign and warmup rates, with ITS OWN
-- denominator (the caller picks all three at once, from
-- warmup.Decision.DrivingBouncePair). The table has one bounce column pair and
-- pooling the two populations to fill it would reintroduce the exact dilution
-- defect this phase exists to remove. bounce_population is NOT NULL in the
-- PARAMETER even though the column is nullable: the column is nullable only for
-- rows written before the split, and a new row that cannot say which population it
-- counted has no business being written. invalid_tokens keeps its Phase 0 column
-- name but now holds the observer-side token-failure count (design §4.5) — the
-- number that column always meant to carry, finally non-zero.
WITH changed AS (
    UPDATE warmup_participants p
    SET health_state = @to_state,
        health_reason = @reason,
        lane = @to_lane,
        lane_reason = sqlc.arg(lane_reason)::text,
        paused_until = sqlc.narg(paused_until)::timestamptz,
        updated_at = now()
    WHERE p.mailbox_id = @mailbox_id
      AND p.workspace_id = @workspace_id
      AND p.health_state = @from_state
      AND p.lane = @from_lane
    RETURNING p.mailbox_id, p.workspace_id
), recorded AS (
    INSERT INTO warmup_state_transitions (
        workspace_id, mailbox_id, from_state, to_state, reason_code, reason,
        from_lane, to_lane, lane_reason_code, lane_reason,
        placement_samples, spam_rate,
        bounce_population, bounce_samples, bounce_rate,
        complaint_samples, complaint_rate, invalid_tokens, policy_version
    )
    SELECT workspace_id, mailbox_id, @from_state, @to_state, @reason_code, @reason,
           @from_lane, @to_lane,
           sqlc.arg(lane_reason_code)::text, sqlc.arg(lane_reason)::text,
           @placement_samples, sqlc.arg(spam_rate)::real,
           sqlc.arg(bounce_population)::text, @bounce_samples, sqlc.arg(bounce_rate)::real,
           @complaint_samples, sqlc.arg(complaint_rate)::real,
           @invalid_tokens, @policy_version
    FROM changed
    RETURNING id
)
SELECT EXISTS(SELECT 1 FROM recorded) AS applied;
