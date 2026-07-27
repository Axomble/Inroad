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
INSERT INTO warmup_participants (
    mailbox_id, workspace_id,
    start_volume, max_volume, ramp_increment, reply_rate
)
SELECT $1, $2, $3, $4, $5, $6
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
    m.email,
    COALESCE(wk.inbox, 0)::bigint AS inbox_7d,
    COALESCE(wk.spam, 0)::bigint  AS spam_7d,
    COALESCE(td.sent, 0)::int     AS today_sent
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id AND m.workspace_id = p.workspace_id
LEFT JOIN (
    SELECT s.mailbox_id, SUM(s.inbox) AS inbox, SUM(s.spam) AS spam
    FROM warmup_daily_stats s
    WHERE s.workspace_id = $1 AND s.day >= CURRENT_DATE - 6
    GROUP BY s.mailbox_id
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
-- config + health, and its decrypted-at-caller transport columns. workspace-pinned
-- (belt-and-braces on the unguessable mailbox UUID); a foreign pair yields no row.
SELECT p.workspace_id, p.enabled, p.start_volume, p.max_volume, p.ramp_increment,
       p.reply_rate, p.started_at, p.health_state, p.paused_until,
       m.provider, m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.allow_plaintext
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id
WHERE p.mailbox_id = $1 AND p.workspace_id = $2;

-- name: SelectWarmupPartner :one
-- Pick ONE eligible warmup partner for a sender: a DIFFERENT, enabled, non-paused
-- participant in the SAME workspace, preferring one not recently paired with the
-- sender. Ordering: least-recently-active shared thread first (a never-paired
-- partner sorts on 'epoch', so it wins), tie-broken deterministically by
-- mailbox_id so partner spread is stable and reproducible. workspace-pinned; a
-- workspace with <2 eligible participants returns no row.
SELECT p.mailbox_id, m.email, m.display_name
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id
WHERE p.workspace_id = $1
  AND p.mailbox_id <> $2
  AND p.enabled
  AND p.health_state <> 'paused'
  AND (p.paused_until IS NULL OR p.paused_until <= now())
ORDER BY (
    SELECT COALESCE(MAX(t.last_activity_at), 'epoch'::timestamptz)
    FROM warmup_threads t
    WHERE t.workspace_id = $1
      AND ((t.sender_mailbox = $2 AND t.partner_mailbox = p.mailbox_id)
        OR (t.sender_mailbox = p.mailbox_id AND t.partner_mailbox = $2))
  ) ASC, p.mailbox_id ASC
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
SELECT p.mailbox_id, m.email, m.display_name,
       t.id AS thread_id, t.content_key, t.turn, t.root_message_id
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id
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
  AND t.turn >= 1
  AND t.turn < sqlc.arg(max_turn)::int
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
INSERT INTO warmup_sends (id, workspace_id, thread_id, from_mailbox, to_mailbox,
                          is_reply, token, status, claimed_at)
SELECT $1, $2, $3, $4, $5, $6, $7, 'sending', now()
FROM mailboxes WHERE id = $4 AND workspace_id = $2
ON CONFLICT (id) DO UPDATE SET status = 'sending', claimed_at = now(), last_error = ''
    WHERE warmup_sends.workspace_id = $2
      AND (warmup_sends.status = 'queued'
        OR (warmup_sends.status = 'sending'
            AND warmup_sends.claimed_at < now() - make_interval(secs => sqlc.arg(lease_seconds)::int)))
RETURNING id;

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
-- returns pgx.ErrNoRows). SELF-ENFORCING tenancy: the INSERT ... SELECT emits a
-- candidate row ONLY when the recipient mailbox truly belongs to the workspace, so
-- a foreign pair also inserts nothing (also pgx.ErrNoRows) — the caller
-- disambiguates duplicate-vs-cross-tenant with GetWarmupReceiptByPair. received_at
-- is returned so the caller seeds the deterministic engage plan on the SAME instant
-- a later GetWarmupEngageJob re-reads. source_folder + message_id are the receipt
-- locator (000019): the provider folder the message was found in and its RFC822
-- Message-ID, so C5b's engager can relocate/rescue/mark-read the exact message.
INSERT INTO warmup_receipts (workspace_id, warmup_send_id, recipient_mailbox, placement, source_folder, message_id)
SELECT $1, $2, $3, $4, $5, $6
FROM mailboxes WHERE id = $3 AND workspace_id = $1
ON CONFLICT (warmup_send_id, recipient_mailbox) DO NOTHING
RETURNING id, received_at;

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
       r.source_folder, r.message_id,
       m.provider, m.imap_host, m.imap_port, m.imap_username,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext,
       m.allow_plaintext, p.reply_rate
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
SELECT t.id AS thread_id, t.turn, t.content_key, t.root_message_id,
       s.from_mailbox AS sender_mailbox,
       sm.email AS sender_email,
       rm.email AS recipient_email,
       rm.display_name AS recipient_name
FROM warmup_receipts r
JOIN warmup_sends s ON s.id = r.warmup_send_id
JOIN warmup_threads t ON t.id = s.thread_id
JOIN mailboxes sm ON sm.id = s.from_mailbox AND sm.workspace_id = r.workspace_id
JOIN mailboxes rm ON rm.id = r.recipient_mailbox AND rm.workspace_id = r.workspace_id
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
-- (mailbox, workspace) for every enabled, non-paused participant — the sweep
-- fan-out. Deliberately coarse: precise ramp/window due-gating is delegated to
-- NextWarmupDue in the send handler (C4), so this only filters out the two states
-- that can never send now (disabled, or paused with a live pause window). A lone
-- participant is returned too; its GetWarmupSendJob then Skips for want of a
-- partner. Global fan-out (no workspace pin), like ListActiveMailboxes.
SELECT mailbox_id, workspace_id FROM warmup_participants
WHERE enabled
  AND health_state <> 'paused'
  AND (paused_until IS NULL OR paused_until <= now())
ORDER BY mailbox_id;

-- name: ListWarmupHealthSignals :many
-- Per-participant trailing-window signals for EvaluateWarmupHealth: the participant's
-- OWN sender-attributed inbox and spam placement sums over the last 7 UTC days, its
-- current health_state, and its paused_until (the timed-block floor gate). The spam
-- placement rate the caller derives is "of MY sent warmup mail, the fraction that
-- landed in spam" = spam / (inbox + spam) — a sender-deliverability signal, NOT the
-- recipient-side received volume. The LEFT JOIN keeps a participant with no recent
-- placement (inbox+spam=0 → spamRate 0 → healthy). Global fan-out (health is
-- recomputed for every enabled participant). Bounce and invalid-token signals have no
-- persistence in the v1 schema, so the caller passes them as zero (documented gap).
SELECT p.mailbox_id, p.workspace_id, p.health_state, p.paused_until,
       COALESCE(SUM(s.inbox), 0)::bigint AS inbox,
       COALESCE(SUM(s.spam), 0)::bigint  AS spam
FROM warmup_participants p
LEFT JOIN warmup_daily_stats s
  ON s.mailbox_id = p.mailbox_id AND s.day >= CURRENT_DATE - 6
WHERE p.enabled
GROUP BY p.mailbox_id, p.workspace_id, p.health_state, p.paused_until;

-- name: UpdateWarmupHealth :exec
-- Persist a health transition for one participant: new state, human-readable
-- reason, and the pause window (NULL clears it on recovery to watch/healthy).
-- workspace-pinned.
UPDATE warmup_participants
SET health_state = $3, health_reason = $4, paused_until = $5, updated_at = now()
WHERE mailbox_id = $1 AND workspace_id = $2;
