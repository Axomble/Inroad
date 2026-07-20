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
SELECT s.id AS send_id, s.workspace_id, s.to_email, s.mailbox_id,
       ct.first_name, cam.subject, cam.body_text, cam.body_html,
       m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.use_tls,
       m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days, m.created_at AS mailbox_created_at
FROM sends s
JOIN campaigns cam ON cam.id = s.campaign_id
JOIN contacts ct ON ct.id = s.contact_id
JOIN mailboxes m ON m.id = s.mailbox_id
WHERE s.id = $1;
-- name: SetSendResult :exec
UPDATE sends SET status = $2, message_id = $3, error = $4,
       sent_at = CASE WHEN $2 = 'sent' THEN now() ELSE sent_at END
WHERE id = $1;
-- name: CountSentToday :one
SELECT count(*) FROM sends
WHERE mailbox_id = $1 AND status = 'sent' AND sent_at::date = (now() AT TIME ZONE 'utc')::date;
-- name: CountQueuedByCampaign :one
SELECT count(*) FROM sends WHERE campaign_id = $1 AND status = 'queued';
-- name: GetCampaignIDForSend :one
SELECT campaign_id, workspace_id FROM sends WHERE id = $1;
-- name: ListStuckQueuedSends :many
-- ListStuckQueuedSends returns sends that are still 'queued' more than two
-- minutes after creation. The sweeper re-enqueues these — a launch that
-- failed to enqueue partway (or a redis blip) would otherwise leave them
-- orphaned. Capped so a single sweep tick can't monopolize the worker.
SELECT id FROM sends
WHERE status = 'queued' AND created_at < now() - interval '2 minutes'
ORDER BY created_at ASC
LIMIT 500;
