-- name: EnqueueSends :many
INSERT INTO sends (workspace_id, campaign_id, contact_id, mailbox_id, to_email)
SELECT cam.workspace_id, cam.id, lm.contact_id, cam.mailbox_id, ct.email
FROM campaigns cam
JOIN list_members lm ON lm.list_id = cam.list_id
JOIN contacts ct ON ct.id = lm.contact_id
WHERE cam.id = $1 AND cam.workspace_id = $2
ON CONFLICT (campaign_id, contact_id) DO NOTHING
RETURNING id;
-- name: GetSendBundle :one
-- Bundle is workspace-scoped: even though sendID is a UUID (unguessable in
-- practice), pinning workspace_id in the WHERE clause forces a not-found
-- verdict if a worker somehow processes a send id from another tenant.
-- Defense in depth on top of the queue-side ownership.
SELECT s.id AS send_id, s.workspace_id, s.to_email, s.mailbox_id, s.attempts,
       ct.first_name, cam.subject, cam.body_text, cam.body_html,
       m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.use_tls,
       m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days, m.created_at AS mailbox_created_at
FROM sends s
JOIN campaigns cam ON cam.id = s.campaign_id
JOIN contacts ct ON ct.id = s.contact_id
JOIN mailboxes m ON m.id = s.mailbox_id
WHERE s.id = $1 AND s.workspace_id = $2;
-- name: SetSendResult :exec
UPDATE sends SET status = $2, message_id = $3, error = $4,
       sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
WHERE id = $1 AND workspace_id = $5;
-- name: CountSentToday :one
-- Sends today for a mailbox, counted over the UTC calendar day. The half-open
-- range is explicitly UTC (date_trunc on now() AT TIME ZONE 'utc'), so it counts
-- the UTC day unconditionally. This matches the old
-- sent_at::date = (now() AT TIME ZONE 'utc')::date only when the session
-- TimeZone is UTC; the new form is in fact more correct, being UTC-day
-- regardless of the session TimeZone. Expressed as a sargable half-open range on
-- sent_at so the partial index idx_sends_mailbox_sent
-- (mailbox_id, sent_at WHERE status='sent') can range-seek instead of casting
-- every row's sent_at. Runs on every advance/send.
SELECT count(*) FROM sends
WHERE mailbox_id = $1 AND status = 'sent'
  AND sent_at >= date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc'
  AND sent_at <  (date_trunc('day', now() AT TIME ZONE 'utc') AT TIME ZONE 'utc') + interval '1 day';
-- name: CountQueuedByCampaign :one
SELECT count(*) FROM sends WHERE campaign_id = $1 AND workspace_id = $2 AND status = 'queued';
-- name: GetCampaignIDForSend :one
SELECT campaign_id, workspace_id FROM sends WHERE id = $1;
-- name: ListStuckQueuedSends :many
-- ListStuckQueuedSends returns sends that are still 'queued' more than two
-- minutes after creation. The sweeper re-enqueues these — a launch that
-- failed to enqueue partway (or a redis blip) would otherwise leave them
-- orphaned. Capped so a single sweep tick can't monopolize the worker.
-- workspace_id travels along so the re-enqueued task carries the pin the
-- worker will use in its subsequent DB lookups.
SELECT id, workspace_id FROM sends
WHERE status = 'queued' AND created_at < now() - interval '2 minutes'
ORDER BY created_at ASC
LIMIT 500;
-- name: IncrementSendAttempts :one
-- Bumps the attempts counter and returns the new value so the worker can
-- decide whether to fail the send instead of re-enqueuing indefinitely.
UPDATE sends SET attempts = attempts + 1
WHERE id = $1 AND workspace_id = $2
RETURNING attempts;
-- name: GetSendByMessageID :one
-- Match an inbound reply/bounce back to the send that caused it, workspace-scoped.
-- sends has no enrollment_id of its own, so this left-joins sequence_enrollments
-- via (campaign_id, contact_id) — unique on that table, so the join is 1:1. A
-- legacy direct send with no active sequence has no enrollment row, so
-- enrollment_id comes back null; the handler treats that as "no enrollment to
-- stop". message_id has no uniqueness constraint, so ORDER BY created_at DESC
-- makes the LIMIT 1 deterministic: the most recent send wins if it's ever
-- non-unique.
SELECT s.id, s.contact_id, s.to_email, e.id AS enrollment_id
FROM sends s
LEFT JOIN sequence_enrollments e
    ON e.campaign_id = s.campaign_id AND e.contact_id = s.contact_id
WHERE s.workspace_id = $1 AND s.message_id = $2 AND s.message_id <> ''
ORDER BY s.created_at DESC
LIMIT 1;
