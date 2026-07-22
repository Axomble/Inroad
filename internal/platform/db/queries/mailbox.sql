-- name: CreateMailbox :one
INSERT INTO mailboxes (
    workspace_id, provider, email, display_name,
    smtp_host, smtp_port, smtp_username,
    imap_host, imap_port, imap_username,
    secret_ciphertext, use_tls,
    daily_cap, min_interval_seconds,
    ramp_enabled, ramp_start_cap, ramp_days
) VALUES (
    $1, $2, $3, $4,
    $5, $6, $7,
    $8, $9, $10,
    $11, $12,
    $13, $14,
    $15, $16, $17
)
RETURNING *;

-- name: GetMailbox :one
SELECT * FROM mailboxes WHERE id = $1 AND workspace_id = $2;

-- name: ListMailboxes :many
SELECT * FROM mailboxes WHERE workspace_id = $1 ORDER BY created_at DESC;

-- name: CountMailboxByEmail :one
SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND email = $2;

-- name: UpdateMailboxStatus :one
UPDATE mailboxes
SET status = $3, last_error = $4
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: DeleteMailbox :execrows
DELETE FROM mailboxes WHERE id = $1 AND workspace_id = $2;

-- name: MailboxExists :one
SELECT EXISTS (SELECT 1 FROM mailboxes WHERE id = $1 AND status = 'active');

-- name: ListActiveMailboxes :many
-- Mailboxes eligible for inbox polling (reply/bounce detection). The poller
-- iterates these and calls GetMailbox per id to get IMAP config + cursor.
SELECT id, workspace_id FROM mailboxes WHERE status = 'active';

-- name: SetInboxCursor :exec
-- Persists the IMAP poll cursor after a poll pass, so the next pass resumes
-- from inbox_last_seen_uid (or resyncs from scratch if inbox_uid_validity
-- has changed underneath it — an IMAP server-side UIDVALIDITY bump).
UPDATE mailboxes SET inbox_last_seen_uid = $3, inbox_uid_validity = $4
WHERE id = $1 AND workspace_id = $2;
