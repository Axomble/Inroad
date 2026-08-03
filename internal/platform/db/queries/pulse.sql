-- Pulse: the workspace-wide aggregate read-model behind GET /pulse. Every
-- query is one COUNT(*) FILTER pass over a workspace-pinned table, so the
-- payload stays O(1) regardless of workspace scale. Polled by every open
-- console session (~45s), so each read must stay index-backed.

-- name: GetPulseMailboxCounts :one
-- error_reason is a deterministic sample (MIN of the non-empty last_error
-- values) so the attention row can state WHY mailboxes are in error, not just
-- how many. Empty when no erroring mailbox recorded a reason.
SELECT COUNT(*)::bigint                                          AS total,
       COUNT(*) FILTER (WHERE status = 'active')::bigint         AS active,
       COUNT(*) FILTER (WHERE status = 'paused')::bigint         AS paused,
       COUNT(*) FILTER (WHERE status = 'error')::bigint          AS error,
       COALESCE(MIN(NULLIF(last_error, '')) FILTER (WHERE status = 'error'), '')::text AS error_reason
FROM mailboxes
WHERE workspace_id = $1;

-- name: GetPulseWarmupCounts :one
-- Enabled participants only: a disabled row's health_state is frozen history,
-- not a live signal (see queries/sender.sql), so it belongs in no bucket.
-- at_risk folds 'throttled' and 'paused' together — both mean the engine is
-- actively holding volume back.
SELECT COUNT(*)::bigint                                                       AS pool,
       COUNT(*) FILTER (WHERE health_state = 'healthy')::bigint               AS healthy,
       COUNT(*) FILTER (WHERE health_state = 'watch')::bigint                 AS watch,
       COUNT(*) FILTER (WHERE health_state IN ('throttled', 'paused'))::bigint AS at_risk
FROM warmup_participants
WHERE workspace_id = $1 AND enabled;

-- name: GetPulseCampaignCounts :one
SELECT COUNT(*)::bigint                                    AS total,
       COUNT(*) FILTER (WHERE status = 'running')::bigint  AS running,
       COUNT(*) FILTER (WHERE status = 'draft')::bigint    AS draft,
       COUNT(*) FILTER (WHERE status = 'paused')::bigint   AS paused
FROM campaigns
WHERE workspace_id = $1;

-- name: CountPulseContacts :one
SELECT COUNT(*)::bigint FROM contacts WHERE workspace_id = $1;

-- name: CountPulseSentToday :one
-- Workspace-wide cold sends for today, on the SAME UTC half-open day window as
-- CountSentToday / the campaign daily limit (queries/send.sql) so the pulse
-- meter and the enforcement agree about what "today" is. Fanned out through
-- mailboxes so each inner count range-seeks idx_sends_mailbox_sent
-- (mailbox_id, sent_at WHERE status='sent') instead of scanning the
-- workspace's whole send history — sends has no (workspace_id, sent_at) index
-- and this read alone would not justify one.
SELECT COALESCE(SUM(c.cnt), 0)::bigint AS sent_today
FROM mailboxes m
CROSS JOIN LATERAL (
    SELECT count(*) AS cnt FROM sends s
    WHERE s.mailbox_id = m.id AND s.status = 'sent'
      AND s.sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
      AND s.sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day'
) c
WHERE m.workspace_id = $1;

-- name: ListPulseSenderCapacity :many
-- One row per ACTIVE mailbox with exactly the inputs sendcap needs (ramp
-- schedule, age, live warmup health), so the pulse daily_cap is computed by the
-- same arithmetic the send path enforces — never a second implementation.
-- health_state '' means "no live signal" (not warming, or participant
-- disabled), same convention as queries/sender.sql.
SELECT m.id,
       m.daily_cap,
       m.ramp_enabled,
       m.ramp_start_cap,
       m.ramp_days,
       m.created_at,
       COALESCE(CASE WHEN wp.enabled THEN wp.health_state END, '')::text AS health_state
FROM mailboxes m
LEFT JOIN warmup_participants wp
       ON wp.mailbox_id = m.id AND wp.workspace_id = m.workspace_id
WHERE m.workspace_id = $1 AND m.status = 'active';

-- name: GetPulseDmarcAttention :one
-- Domains with ACTIVE senders whose last COMPLETED DNS check found no DMARC
-- record. checked_at IS NOT NULL keeps 'unknown' (never checked / resolver
-- blip) out: reporting an unchecked domain as failing would send operators
-- editing DNS that may be fine (see migration 000036). Domain list derived
-- from mailboxes.email exactly like queries/sendingdomain.sql. sample_domain
-- names one offender so the attention row can carry a truthful reason.
SELECT COUNT(*)::bigint AS count,
       COALESCE(MIN(t.domain), '')::text AS sample_domain
FROM (
    SELECT lower(split_part(m.email, '@', 2)) AS domain
    FROM mailboxes m
    JOIN sending_domains d
      ON d.workspace_id = m.workspace_id
     AND d.domain = lower(split_part(m.email, '@', 2))
    WHERE m.workspace_id = $1
      AND m.status = 'active'
      AND position('@' in m.email) > 0
      AND d.checked_at IS NOT NULL
      AND d.dmarc_found = FALSE
    GROUP BY 1
) t;
