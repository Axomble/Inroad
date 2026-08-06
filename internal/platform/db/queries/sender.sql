-- name: ListCampaignSenders :many
-- A campaign's whole sender pool for display: membership and weights joined to
-- the mailbox identity the UI shows, the rotation state so an operator can see
-- the spread actually happening, and each mailbox's warmup health plus its
-- cap/ramp config and today's send count so the panel can explain a campaign
-- sending slower than configured. Disabled rows are INCLUDED — the panel edits
-- them. Workspace-pinned, and the mailbox join is pinned too so a row can never
-- surface another tenant's mailbox.
--
-- health_state is '' for a mailbox that is not warming up, which includes a
-- DISABLED warmup participant (the API reports that absence as null): the health sweep only recomputes health for
-- enabled participants, so a disabled row's state is frozen history rather than a
-- live signal, and gating cold sending on it forever would be a silent trap. The
-- send path reads health the same way (ListCampaignSenderCandidates), so the
-- reported cap is the one that will actually be enforced.
--
-- cap_today is deliberately NOT computed here: ramp and health scaling live in
-- platform/sendcap so the API and the sender cannot disagree about a mailbox's
-- capacity.
SELECT cs.mailbox_id, cs.weight, cs.enabled, cs.assigned_count, cs.last_assigned_at,
       m.email, m.provider, m.status,
       m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days,
       m.created_at AS mailbox_created_at,
       COALESCE(CASE WHEN wp.enabled THEN wp.health_state END, '')::text AS health_state,
       (SELECT count(*) FROM sends s
         WHERE s.mailbox_id = cs.mailbox_id AND s.status = 'sent'
           AND s.sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
           AND s.sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day'
       ) AS sent_today
FROM campaign_senders cs
JOIN mailboxes m ON m.id = cs.mailbox_id AND m.workspace_id = cs.workspace_id
LEFT JOIN warmup_participants wp ON wp.mailbox_id = cs.mailbox_id AND wp.workspace_id = cs.workspace_id
WHERE cs.campaign_id = $1 AND cs.workspace_id = $2
ORDER BY m.email;

-- name: GetCampaignFallbackSender :one
-- The implicit one-mailbox pool a campaign with NO campaign_senders rows sends
-- from (campaigns.mailbox_id). An empty pool means "never configured", not
-- "broken", so the read path projects the fallback in the same shape as a real
-- pool row rather than reporting a campaign with no senders it will in fact send
-- from — including the health and capacity columns, since the fallback mailbox is
-- gated by its warmup health exactly like a pool member. Workspace-pinned on both
-- the campaign and the mailbox join.
SELECT cam.mailbox_id, m.email, m.provider, m.status,
       m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days,
       m.created_at AS mailbox_created_at,
       COALESCE(CASE WHEN wp.enabled THEN wp.health_state END, '')::text AS health_state,
       (SELECT count(*) FROM sends s
         WHERE s.mailbox_id = cam.mailbox_id AND s.status = 'sent'
           AND s.sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
           AND s.sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day'
       ) AS sent_today
FROM campaigns cam
JOIN mailboxes m ON m.id = cam.mailbox_id AND m.workspace_id = cam.workspace_id
LEFT JOIN warmup_participants wp ON wp.mailbox_id = cam.mailbox_id AND wp.workspace_id = cam.workspace_id
WHERE cam.id = $1 AND cam.workspace_id = $2;

-- name: GetMailboxColdHealth :one
-- One mailbox's warmup health for the COLD send path, workspace-pinned. Read for
-- the sender a step actually resolved to — the enrollment's pinned mailbox, or a
-- freshly selected pool member — so health gating applies to a thread already in
-- flight and not only at assignment time. Empty means "no live health signal":
-- not a warmup participant, a disabled participant (whose stored state is frozen,
-- see ListCampaignSenders), or a mailbox that has been deleted.
SELECT COALESCE((SELECT CASE WHEN wp.enabled THEN wp.health_state ELSE '' END
                 FROM warmup_participants wp
                 WHERE wp.mailbox_id = $1 AND wp.workspace_id = $2), '')::text AS health_state;

-- name: ListCampaignSenderCandidates :many
-- Every pool row with the state rotation needs: the operator's weight/enabled
-- flags, rotation counters, the mailbox's status and cap/ramp config, its warmup
-- health, and how much it has already sent today. Eligibility (enabled, active,
-- not health-paused, under its health-scaled cap) is decided in Go so that
-- remaining capacity has exactly ONE implementation (platform/sendcap) and so an
-- exhausted pool can still report its aggregate capacity for the cap-deferral
-- path. Ordered by mailbox_id, matching rotation's tie-break.
--
-- health_state is '' when there is no LIVE health signal: not a warmup
-- participant, or a disabled one — the health sweep only recomputes health for
-- enabled participants, so a disabled row's state is frozen history, and gating
-- cold sending on it would block the mailbox forever with no engine able to
-- recover it.
--
-- sent_today repeats CountSentToday's UTC-day half-open range verbatim so a
-- mailbox's cap means the same thing here as on the single-mailbox path, and so
-- the idx_sends_mailbox_sent partial index can range-seek per candidate.
--
-- provider and smtp_host travel for ESP matching, and BOTH are needed: provider
-- is a transport tag (smtp|gmail|m365) that selects a code path, not an ESP — a
-- Google Workspace mailbox connected by app password is provider='smtp' — so the
-- host is the only evidence for that case. No secrets are projected; smtp_host
-- is a public endpoint name.
SELECT cs.mailbox_id, cs.weight, cs.enabled, cs.assigned_count, cs.last_assigned_at,
       m.provider, m.smtp_host,
       m.status AS mailbox_status, m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days,
       m.created_at AS mailbox_created_at,
       COALESCE(CASE WHEN wp.enabled THEN wp.health_state END, '')::text AS health_state,
       (SELECT count(*) FROM sends s
         WHERE s.mailbox_id = cs.mailbox_id AND s.status = 'sent'
           AND s.sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
           AND s.sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day'
       ) AS sent_today
FROM campaign_senders cs
JOIN mailboxes m ON m.id = cs.mailbox_id AND m.workspace_id = cs.workspace_id
LEFT JOIN warmup_participants wp ON wp.mailbox_id = cs.mailbox_id AND wp.workspace_id = cs.workspace_id
WHERE cs.campaign_id = $1 AND cs.workspace_id = $2
ORDER BY cs.mailbox_id;

-- name: UpsertCampaignSender :batchexec
-- One pool member per call, pipelined as a single batch. ON CONFLICT updates only
-- the operator-owned columns, so assigned_count/last_assigned_at SURVIVE a pool
-- edit for a mailbox that stays in it — otherwise every weight tweak would reset
-- the rotation. The composite tenant FK rejects a mailbox from another workspace
-- outright, behind the service's 422.
INSERT INTO campaign_senders (workspace_id, campaign_id, mailbox_id, weight, enabled)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (campaign_id, mailbox_id)
DO UPDATE SET weight = EXCLUDED.weight, enabled = EXCLUDED.enabled;

-- name: DeleteCampaignSendersExcept :exec
-- Drops the members the replace left out. Paired with UpsertCampaignSender in one
-- transaction, upsert first, so the pool is never observed empty mid-replace (an
-- empty pool reads as "never configured" and falls back to campaigns.mailbox_id).
DELETE FROM campaign_senders
WHERE campaign_id = $1 AND workspace_id = $2
  AND NOT (mailbox_id = ANY(sqlc.arg(mailbox_ids)::uuid[]));

-- name: BumpCampaignSenderAssignment :exec
-- Records that this mailbox took one more contact. Runs in the SAME transaction
-- as the enrollment claim, so rotation state cannot drift from the assignments
-- that actually happened.
UPDATE campaign_senders
SET assigned_count = assigned_count + 1, last_assigned_at = now()
WHERE campaign_id = $1 AND workspace_id = $2 AND mailbox_id = $3;

-- name: ClaimEnrollmentMailbox :one
-- The write-once assignment claim: pin the enrollment's sending mailbox only if
-- it has none yet. Zero rows (pgx.ErrNoRows) means another worker claimed it
-- first, so the caller RE-READS the stored value rather than recomputing — two
-- concurrent assigners can never disagree, and a retry reads instead of
-- selecting again. RETURNING id (not mailbox_id) because the interesting fact is
-- whether we won the row; the value is $3 by construction and a nullable
-- RETURNING would force a pointless nil check.
UPDATE sequence_enrollments SET mailbox_id = $3
WHERE id = $1 AND workspace_id = $2 AND mailbox_id IS NULL
RETURNING id;

-- name: GetEnrollmentMailbox :one
-- Re-read after a lost claim: the winner's mailbox is the thread's mailbox.
SELECT mailbox_id FROM sequence_enrollments WHERE id = $1 AND workspace_id = $2;
