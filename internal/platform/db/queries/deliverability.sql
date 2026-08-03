-- Deliverability guardrails: the counts the score and the circuit breaker are
-- computed from, the breaker's pause transition, and the event-ingest write.
--
-- Every statement is workspace-pinned (the workspace comes from the JWT at the
-- handler, or from the task payload in the worker — never from a request body).
-- NUMERIC columns are read and written through an explicit ::float8 cast so the
-- percentages stay plain float64 in Go: they are already bounded to 0.1..100 by
-- CHECK constraints, so arbitrary-precision decimal buys nothing but conversion
-- code at every boundary.

-- name: GetCampaignGuardrails :one
-- The breaker's configuration plus the campaign's current status, in one read:
-- the breaker needs both (it only pauses a RUNNING campaign) and so does the
-- campaign deliverability endpoint (a paused campaign reports verdict 'paused').
-- Zero rows means the campaign does not exist in this workspace — the 404.
SELECT auto_pause_enabled,
       bounce_pause_pct::float8    AS bounce_pause_pct,
       complaint_pause_pct::float8 AS complaint_pause_pct,
       status
FROM campaigns
WHERE id = @campaign_id AND workspace_id = @workspace_id;

-- name: SetCampaignGuardrails :execrows
-- Zero affected rows means the campaign is not this workspace's — the 404. The
-- percentages are validated in the service (422) and CHECK-constrained here, so a
-- caller bypassing Go still cannot store a threshold of 0.
UPDATE campaigns
SET auto_pause_enabled  = @auto_pause_enabled,
    bounce_pause_pct    = sqlc.arg(bounce_pause_pct)::float8::numeric,
    complaint_pause_pct = sqlc.arg(complaint_pause_pct)::float8::numeric
WHERE id = @campaign_id AND workspace_id = @workspace_id;

-- name: PauseCampaignForBreach :execrows
-- The breaker's transition, guarded on status='running'. That guard is the
-- exactly-once guarantee: a second evaluation of an already-paused campaign
-- updates zero rows, so it records no second pause event. It reuses the existing
-- 'paused' status rather than inventing a terminal state — an auto-paused
-- campaign is a paused campaign, restartable the normal way.
UPDATE campaigns SET status = 'paused'
WHERE id = @campaign_id AND workspace_id = @workspace_id AND status = 'running';

-- name: InsertCampaignPauseEvent :exec
-- Why a campaign stopped. Written in the SAME transaction as the status flip, so
-- a paused campaign always has its explanation (invariant 3).
INSERT INTO campaign_pause_events (
    workspace_id, campaign_id, reason, metric, value, threshold, delivered
) VALUES (
    @workspace_id, @campaign_id, @reason, @metric,
    sqlc.arg(value)::float8::numeric, sqlc.arg(threshold)::float8::numeric,
    @delivered
);

-- name: ListCampaignPauseEvents :many
-- This campaign's pause history, newest first, for the campaign detail card.
SELECT reason, metric, value::float8 AS value, threshold::float8 AS threshold,
       delivered, created_at
FROM campaign_pause_events
WHERE workspace_id = @workspace_id AND campaign_id = @campaign_id
ORDER BY created_at DESC
LIMIT @row_limit;

-- name: LatestCampaignPauseAt :one
-- When this campaign was last auto-paused, or NULL if never. It is the lower
-- bound on the evidence a fresh evaluation may act on: an operator who restarts a
-- paused campaign has judged the evidence that paused it, and re-pausing on that
-- same evidence would override a human decision with no new information.
SELECT MAX(created_at)::timestamptz AS paused_at
FROM campaign_pause_events
WHERE workspace_id = @workspace_id AND campaign_id = @campaign_id;

-- name: GetCampaignDeliverabilityCounts :one
-- One campaign's evidence over a half-open window [since, now).
--
-- delivered counts SENDS; bounced counts DISTINCT CONTACTS across both bounce
-- sources (an enrollment stopped 'bounced' by the inbox poller, and an ingested
-- provider bounce attributed through its send). Counting contacts is what dedups
-- the two feeds — a bounce Inroad detected AND the provider reported must not
-- count twice and double the rate the breaker acts on.
--
-- The mixed unit (bounced contacts over delivered sends) understates the rate on
-- a multi-step campaign, because a bounced contact stops after one or two sends
-- while a healthy one keeps receiving them. That error is in the safe direction:
-- it can only make the breaker slower to fire, never quicker, and on the early
-- sends where bounces actually happen the two units coincide.
--
-- complained counts EVENTS (each provider_event_id is unique, so there is nothing
-- to dedup) and only those attributable to this campaign through send_id; an
-- event the provider reported without one counts toward the workspace rollup but
-- toward no campaign's breaker.
--
-- complaint_feed reports whether a complaint feed has EVER reported for this
-- workspace, over all time and regardless of window. It is what separates
-- measured-and-clean (0 complaints from a live feed) from NOT MEASURED (nobody
-- ever looked) — invariant 4. Without it a workspace with no feed would read as
-- having a perfect complaint rate.
SELECT
    (SELECT COUNT(*) FROM sends s
      WHERE s.workspace_id = @workspace_id AND s.campaign_id = @campaign_id
        AND s.status = 'sent' AND s.sent_at >= @since)::bigint AS delivered,
    (SELECT COUNT(*) FROM (
        SELECT e.contact_id FROM sequence_enrollments e
         WHERE e.workspace_id = @workspace_id AND e.campaign_id = @campaign_id
           AND e.stop_reason = 'bounced' AND e.stopped_at >= @since
        UNION
        SELECT s.contact_id FROM deliverability_events d
          JOIN sends s ON s.id = d.send_id AND s.workspace_id = d.workspace_id
         WHERE d.workspace_id = @workspace_id AND d.kind = 'bounce'
           AND d.received_at >= @since AND s.campaign_id = @campaign_id
    ) b)::bigint AS bounced,
    (SELECT COUNT(*) FROM deliverability_events d
       JOIN sends s ON s.id = d.send_id AND s.workspace_id = d.workspace_id
      WHERE d.workspace_id = @workspace_id AND d.kind = 'complaint'
        AND d.received_at >= @since AND s.campaign_id = @campaign_id)::bigint AS complained,
    EXISTS (SELECT 1 FROM deliverability_events d
             WHERE d.workspace_id = @workspace_id AND d.kind = 'complaint') AS complaint_feed;

-- name: GetWorkspaceDeliverabilityCounts :one
-- The workspace rollup's evidence over the same half-open window. Same counting
-- rules as the per-campaign version (see above); complaints need no send join
-- here, so an event the provider reported without a send_id IS counted — at
-- workspace scope there is nothing to attribute it to.
SELECT
    (SELECT COUNT(*) FROM sends s
      WHERE s.workspace_id = @workspace_id AND s.status = 'sent' AND s.sent_at >= @since)::bigint AS delivered,
    (SELECT COUNT(*) FROM (
        SELECT e.contact_id FROM sequence_enrollments e
         WHERE e.workspace_id = @workspace_id
           AND e.stop_reason = 'bounced' AND e.stopped_at >= @since
        UNION
        SELECT s.contact_id FROM deliverability_events d
          JOIN sends s ON s.id = d.send_id AND s.workspace_id = d.workspace_id
         WHERE d.workspace_id = @workspace_id AND d.kind = 'bounce' AND d.received_at >= @since
    ) b)::bigint AS bounced,
    (SELECT COUNT(*) FROM deliverability_events d
      WHERE d.workspace_id = @workspace_id AND d.kind = 'complaint'
        AND d.received_at >= @since)::bigint AS complained,
    EXISTS (SELECT 1 FROM deliverability_events d
             WHERE d.workspace_id = @workspace_id AND d.kind = 'complaint') AS complaint_feed;

-- name: GetDeliverabilitySignals :one
-- The non-rate signals: warmup spam-vs-inbox placement, the worst warmup health,
-- and the worst sending-domain verdict, over a set of mailboxes.
--
-- mailbox_ids EMPTY means "every mailbox in the workspace" (the workspace
-- rollup); a non-empty array scopes it to one campaign's sender pool. One
-- statement serves both so the two reports cannot drift apart.
--
-- Placement is attributed to the SENDER (warmup_daily_stats rows are already
-- sender-attributed by RecordWarmupSenderPlacementStat), because inbox-vs-spam is
-- a fact about whoever SENT the mail, not whoever observed where it landed.
--
-- Both health and domain state take the WORST value among the mailboxes rather
-- than an average: one paused mailbox in a pool of five is a real problem, and
-- averaging it away is how a degraded sender stays invisible. '' means no signal
-- at all (no participant / no checked domain), which the score treats as NOT
-- MEASURED rather than as healthy.
SELECT
    COALESCE((SELECT SUM(w.inbox) FROM warmup_daily_stats w
               WHERE w.workspace_id = @workspace_id AND w.day >= @since::date
                 AND (cardinality(@mailbox_ids::uuid[]) = 0
                      OR w.mailbox_id = ANY(@mailbox_ids::uuid[]))), 0)::bigint AS inbox_placed,
    COALESCE((SELECT SUM(w.spam) FROM warmup_daily_stats w
               WHERE w.workspace_id = @workspace_id AND w.day >= @since::date
                 AND (cardinality(@mailbox_ids::uuid[]) = 0
                      OR w.mailbox_id = ANY(@mailbox_ids::uuid[]))), 0)::bigint AS spam_placed,
    COALESCE((SELECT p.health_state FROM warmup_participants p
               WHERE p.workspace_id = @workspace_id AND p.enabled
                 AND (cardinality(@mailbox_ids::uuid[]) = 0
                      OR p.mailbox_id = ANY(@mailbox_ids::uuid[]))
               ORDER BY CASE p.health_state
                          WHEN 'paused' THEN 4 WHEN 'throttled' THEN 3 WHEN 'watch' THEN 2 ELSE 1 END DESC
               LIMIT 1), '')::text AS warmup_state,
    COALESCE((SELECT d.state FROM sending_domains d
               WHERE d.workspace_id = @workspace_id
                 AND d.domain IN (
                     SELECT lower(split_part(m.email, '@', 2)) FROM mailboxes m
                      WHERE m.workspace_id = @workspace_id
                        AND (cardinality(@mailbox_ids::uuid[]) = 0
                             OR m.id = ANY(@mailbox_ids::uuid[])))
               ORDER BY CASE d.state
                          WHEN 'failing' THEN 3 WHEN 'unknown' THEN 2 ELSE 1 END DESC
               LIMIT 1), '')::text AS domain_state;

-- name: ListCampaignSenderMailboxes :many
-- The mailboxes a campaign actually sends from: its sender pool, or — when it has
-- no pool rows — the single campaigns.mailbox_id the send path falls back to.
-- Mirrors the resolution order in GetStepSendJob, so the signals a campaign is
-- scored on come from the mailboxes its mail really leaves through.
SELECT cs.mailbox_id FROM campaign_senders cs
 WHERE cs.campaign_id = @campaign_id AND cs.workspace_id = @workspace_id
UNION
SELECT c.mailbox_id FROM campaigns c
 WHERE c.id = @campaign_id AND c.workspace_id = @workspace_id
   AND NOT EXISTS (SELECT 1 FROM campaign_senders cs2
                    WHERE cs2.campaign_id = c.id AND cs2.workspace_id = c.workspace_id);

-- name: ListDeliverabilitySeries :many
-- The workspace's per-day series for the window: one row per UTC day from since to
-- today, including days with no activity (generate_series drives it), so the chart
-- shows a gap as a gap rather than closing it up.
--
-- Workspace-wide only. The frozen CampaignDeliverability schema carries no series,
-- so a per-campaign variant would be a filter nothing can ask for.
--
-- The complained and spam_placed counts are returned unconditionally; the caller
-- nils them out when the corresponding score component was NOT MEASURED, so the
-- series can never imply a rate the score itself declined to claim.
--
-- Each day's bounds are half-open and stated in UTC EXPLICITLY (`AT TIME ZONE
-- 'UTC'`) rather than by letting Postgres widen a DATE with the session time zone:
-- the whole product counts days in UTC (warmup_daily_stats.day, the daily caps,
-- the campaign limit), and a server whose session TZ was not UTC would otherwise
-- silently shift every bucket. Both bounds stay plain comparisons against the bare
-- column, so the existing sent_at indexes are still usable.
WITH days AS (
    SELECT generate_series(@since::date, CURRENT_DATE, INTERVAL '1 day')::date AS day
),
spans AS (
    SELECT day,
           (day::timestamp       AT TIME ZONE 'UTC') AS from_ts,
           ((day + 1)::timestamp AT TIME ZONE 'UTC') AS to_ts
    FROM days
)
SELECT
    d.day,
    (SELECT COUNT(*) FROM sends s
      WHERE s.workspace_id = @workspace_id AND s.status = 'sent'
        AND s.sent_at >= d.from_ts AND s.sent_at < d.to_ts)::bigint AS delivered,
    (SELECT COUNT(*) FROM (
        SELECT e.contact_id FROM sequence_enrollments e
         WHERE e.workspace_id = @workspace_id AND e.stop_reason = 'bounced'
           AND e.stopped_at >= d.from_ts AND e.stopped_at < d.to_ts
        UNION
        SELECT s.contact_id FROM deliverability_events ev
          JOIN sends s ON s.id = ev.send_id AND s.workspace_id = ev.workspace_id
         WHERE ev.workspace_id = @workspace_id AND ev.kind = 'bounce'
           AND ev.received_at >= d.from_ts AND ev.received_at < d.to_ts
    ) b)::bigint AS bounced,
    (SELECT COUNT(*) FROM deliverability_events ev
      WHERE ev.workspace_id = @workspace_id AND ev.kind = 'complaint'
        AND ev.received_at >= d.from_ts AND ev.received_at < d.to_ts)::bigint AS complained,
    COALESCE((SELECT SUM(w.spam) FROM warmup_daily_stats w
               WHERE w.workspace_id = @workspace_id AND w.day = d.day), 0)::bigint AS spam_placed
FROM spans d
ORDER BY d.day;

-- name: ListAtRiskMailboxes :many
-- Mailboxes the warmup engine has already judged degraded, for the dashboard's
-- at-risk list. Worst first so the operator reads the urgent one at the top.
SELECT m.email, p.health_state, p.health_reason
FROM warmup_participants p
JOIN mailboxes m ON m.id = p.mailbox_id AND m.workspace_id = p.workspace_id
WHERE p.workspace_id = @workspace_id AND p.enabled
  AND p.health_state IN ('watch','throttled','paused')
ORDER BY CASE p.health_state
           WHEN 'paused' THEN 3 WHEN 'throttled' THEN 2 ELSE 1 END DESC, m.email;

-- name: ListAtRiskDomains :many
-- Sending domains whose last COMPLETED check said failing. 'unknown' is excluded:
-- a resolver that could not answer is not evidence of a misconfiguration, and
-- listing it would send operators editing DNS that was already correct.
SELECT domain, spf_found, dmarc_found
FROM sending_domains
WHERE workspace_id = @workspace_id AND state = 'failing'
ORDER BY domain;

-- name: InsertDeliverabilityEvent :execrows
-- The ingest write. ON CONFLICT DO NOTHING on (workspace_id, provider_event_id)
-- makes a retried webhook a no-op rather than a second row, so a redelivered
-- event cannot inflate the rate the breaker reads. Zero affected rows IS the
-- duplicate signal the handler turns into 200-instead-of-202.
--
-- The send is resolved by a workspace-pinned SELECT rather than trusted from the
-- request: a caller-supplied send_id belonging to another tenant (or to nothing)
-- stores NULL instead of failing the FK, so the event is still recorded and
-- counted at workspace scope — it simply attributes to no campaign.
INSERT INTO deliverability_events (workspace_id, kind, email, send_id, provider_event_id)
SELECT @workspace_id, @kind, @email,
       (SELECT s.id FROM sends s
         WHERE s.id = sqlc.narg(send_id)::uuid AND s.workspace_id = @workspace_id),
       @provider_event_id
ON CONFLICT (workspace_id, provider_event_id) DO NOTHING;

-- name: GetSendCampaign :one
-- The campaign a send belongs to, workspace-pinned. Used after an ingest to
-- decide which campaign's breaker to re-evaluate: a complaint spike can happen
-- with no new sends, so the ingest has to trigger an evaluation of its own.
SELECT campaign_id FROM sends WHERE id = @send_id AND workspace_id = @workspace_id;
