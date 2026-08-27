-- name: SetSendResult :exec
-- Finalize a send to its terminal state ('sent'|'failed'|'skipped'); sent_at is
-- stamped only on success. Doubles as FinalizeStepSend: the step path calls it
-- inside the same tx as the enrollment cursor advance. workspace_id pinned.
UPDATE sends SET status = $2, message_id = $3, error = $4,
       sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
WHERE id = $1 AND workspace_id = $5;

-- name: GetSendState :one
-- Read a claimed send's current terminal-relevant state, workspace-pinned. Used
-- on the step path AFTER a lost claim: the recover-forward decision keys on
-- status ('sent' ⇒ this step already delivered, advance the cursor without
-- re-sending), and the step-1 thread-root advance reads message_id from the
-- already-'sent' row rather than a fresh send result.
SELECT status, message_id FROM sends WHERE id = $1 AND workspace_id = $2;

-- name: ReleaseSend :exec
-- Release a claimed-but-not-finalized send after a RETRYABLE failure: expire the
-- lease (claimed_at='epoch') so the asynq retry reclaims promptly. Guarded on
-- status='sending' so it never touches a row already finalized by this or
-- another worker. Used by the step path (ReleaseStepSend). workspace_id pinned.
UPDATE sends SET claimed_at = 'epoch'
WHERE id = $1 AND workspace_id = $2 AND status = 'sending';
-- name: CountSentToday :one
-- Sends today for a mailbox, counted over the UTC calendar day, PLUS today's
-- manually-sent inbox replies (POST /inbox/threads/{id}/reply). A reply from
-- the unified inbox is never itself a `sends` row -- sends.campaign_id/
-- contact_id are NOT NULL with a UNIQUE(campaign_id, contact_id), which a
-- reply on a legacy direct-send thread (no campaign/contact) cannot satisfy,
-- and which would collide with the real send row on a threaded one -- but it
-- still consumes the mailbox's real daily volume, so this gate (the daily-cap
-- enforcement the sender pool and step-send job read, per docs/security.md's
-- "a manual reply is never blocked by the cap, but it counts toward it")
-- must see it too. Manual replies are stored as direction='outbound'
-- inbox_messages rows (see internal/app/inbox.Service.Reply /
-- RecordOutboundReply); found here via inbox_threads.mailbox_id, using the
-- plain mailbox_id index migration 000049 added (this call has no
-- workspace_id to use the existing composite one — see below).
--
-- The half-open range is explicitly UTC (date_trunc on now() AT TIME ZONE
-- 'utc'), so it counts the UTC day unconditionally. This matches the old
-- sent_at::date = (now() AT TIME ZONE 'utc')::date only when the session
-- TimeZone is UTC; the new form is in fact more correct, being UTC-day
-- regardless of the session TimeZone. The sends half is expressed as a
-- sargable half-open range on sent_at so the partial index
-- idx_sends_mailbox_sent (mailbox_id, sent_at WHERE status='sent') can
-- range-seek instead of casting every row's sent_at. Runs on every
-- advance/send. Workspace-pinned (this query is not, being keyed on an
-- unguessable mailbox id).
--
-- PERFORMANCE NOTE: the inbox_messages half is NOT as tight as the sends
-- half — idx_inbox_threads_mailbox (migration 000049) narrows by mailbox_id
-- but not by date, so the join's outer side is every thread the mailbox has
-- EVER had, not just today's, before idx_inbox_messages_thread narrows each
-- to today's occurred_at range. Fine at manual-reply volumes (an operator
-- hand-sending replies, not a bulk campaign), but if that ever changes, the
-- escape hatch is denormalizing mailbox_id onto inbox_messages directly (a
-- migration + backfill + a partial index on (mailbox_id, occurred_at) WHERE
-- direction='outbound'), which would make this half scale with reply volume
-- instead of total thread history.
SELECT (
  (SELECT count(*) FROM sends s
   WHERE s.mailbox_id = sqlc.arg('mailbox_id')::uuid AND s.status = 'sent'
     AND s.sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
     AND s.sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day')
  +
  (SELECT count(*) FROM inbox_messages im
   JOIN inbox_threads t ON t.id = im.thread_id
   WHERE t.mailbox_id = sqlc.arg('mailbox_id')::uuid AND im.direction = 'outbound'
     AND im.occurred_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
     AND im.occurred_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day')
)::bigint;
-- name: GetCampaignIDForSend :one
-- sent_at is selected for the tracking classifier's prefetch-window rule (an
-- open within seconds of the send is a machine fetch, not a read). It is NULL
-- for a send that has not gone out, which the classifier reads as "unknown send
-- time" and treats as no signal rather than guessing.
SELECT campaign_id, workspace_id, sent_at FROM sends WHERE id = $1;
-- name: GetSendByMessageID :one
-- Match an inbound reply/bounce back to the send that caused it, workspace-scoped.
-- sends has no enrollment_id of its own, so this left-joins sequence_enrollments
-- via (campaign_id, contact_id) — unique on that table, so the join is 1:1. A
-- legacy direct send with no active sequence has no enrollment row, so
-- enrollment_id comes back null; the handler treats that as "no enrollment to
-- stop". message_id has no uniqueness constraint, so ORDER BY created_at DESC
-- makes the LIMIT 1 deterministic: the most recent send wins if it's ever
-- non-unique. mailbox_id/campaign_id/message_id are additionally selected so the
-- inbox-poll worker can store the matched reply against its mailbox/campaign and
-- anchor the thread on the send's own outbound Message-ID (root_message_id).
SELECT s.id, s.contact_id, s.to_email, s.mailbox_id, s.campaign_id, s.message_id, e.id AS enrollment_id
FROM sends s
LEFT JOIN sequence_enrollments e
    ON e.campaign_id = s.campaign_id AND e.contact_id = s.contact_id
WHERE s.workspace_id = $1 AND s.message_id = $2 AND s.message_id <> ''
ORDER BY s.created_at DESC
LIMIT 1;

-- name: CountCampaignSentToday :one
-- Sends today for a whole CAMPAIGN, counted over the UTC calendar day — the
-- consumption side of campaigns.daily_limit. Deliberately a campaign-wide total
-- across every mailbox in the pool, which is what an operator means by "this
-- campaign sends at most 100/day"; the per-mailbox ceiling is CountSentToday's
-- job. The half-open UTC range repeats CountSentToday's verbatim so a day means
-- the same thing on both gates, and so the partial index
-- idx_sends_campaign_sent (campaign_id, sent_at WHERE status='sent') can
-- range-seek today's rows instead of filtering the campaign's whole history.
-- Workspace-pinned (CountSentToday is not, being keyed on an unguessable mailbox
-- id; every new query is).
SELECT count(*) FROM sends
WHERE campaign_id = $1 AND workspace_id = $2 AND status = 'sent'
  AND sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
  AND sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day';
