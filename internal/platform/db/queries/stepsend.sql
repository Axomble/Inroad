-- name: GetStepEnrollmentBundle :one
-- Everything needed to build one step-send job, workspace-pinned. Joins the
-- enrollment to its campaign, contact and (via the campaign) mailbox. The
-- step content itself is fetched separately by step_order.
SELECT e.id AS enrollment_id, e.workspace_id, e.contact_id, e.current_step,
       e.status, e.thread_root_id,
       cam.id AS campaign_id, cam.mailbox_id, cam.tracking_enabled,
       ct.email AS to_email, ct.first_name, ct.last_name, ct.company, ct.custom_fields,
       m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.use_tls,
       m.daily_cap, m.ramp_enabled, m.ramp_start_cap, m.ramp_days,
       m.created_at AS mailbox_created_at
FROM sequence_enrollments e
JOIN campaigns cam ON cam.id = e.campaign_id
JOIN contacts ct ON ct.id = e.contact_id
JOIN mailboxes m ON m.id = cam.mailbox_id
WHERE e.id = $1 AND e.workspace_id = $2;

-- name: RecordStepSend :one
-- Insert the send row for one step WITH its result in a single write (the
-- advance handler sends first, then records). Keeps GetStepSendJob read-only so
-- a suppressed/capped step never leaves an orphan queued row. sent_at is set
-- only on success. ON CONFLICT makes a duplicate advance a no-op against the
-- (campaign, contact, step_order) idempotency index (migration 000008): the
-- duplicate inserts no row and returns none (sql.ErrNoRows), which the caller
-- treats as already-recorded.
-- id is supplied by the caller (generated in GetStepSendJob, before the step is
-- sent) rather than left to the column default: the worker embeds this same id
-- in the tracking pixel/click tokens at MIME-build time, so the eventual send
-- row's id must be known ahead of the insert for tracking_events to line up.
INSERT INTO sends (id, workspace_id, campaign_id, contact_id, mailbox_id, to_email,
                   step_order, references_header, status, message_id, error, sent_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, CASE WHEN $9 = 'sent' THEN now() ELSE NULL END)
ON CONFLICT (campaign_id, contact_id, step_order) WHERE step_order IS NOT NULL DO NOTHING
RETURNING id;

-- name: LatestSentForContact :one
-- The most recent successfully-sent step for a (campaign, contact), used to
-- thread the next step (In-Reply-To = its message_id; References = its chain).
SELECT message_id, references_header FROM sends
WHERE campaign_id = $1 AND contact_id = $2 AND status = 'sent'
ORDER BY step_order DESC
LIMIT 1;
