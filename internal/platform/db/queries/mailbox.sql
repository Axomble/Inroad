-- name: CreateMailbox :one
INSERT INTO mailboxes (
    workspace_id, provider, email, display_name,
    smtp_host, smtp_port, smtp_username,
    imap_host, imap_port, imap_username,
    secret_ciphertext, allow_plaintext,
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
-- Pause/Resume. Clearing the inbox-poll backoff here makes pause→resume the
-- operator's "try it again now" gesture: a mailbox parked on the hour-long cap
-- because its password is wrong is otherwise up to an hour from noticing that
-- the password was fixed. Pause clearing it too is harmless — a paused mailbox
-- is not polled at all.
UPDATE mailboxes
SET status = $3, last_error = $4, inbox_poll_failures = 0, inbox_poll_retry_after = NULL
WHERE id = $1 AND workspace_id = $2
RETURNING *;

-- name: ReserveMailboxSendSlot :one
-- Atomically enforces per-mailbox spacing across concurrent workers. Updating
-- last_send_at is the reservation: only one worker can satisfy the predicate
-- for a mailbox during the configured interval.
UPDATE mailboxes
SET last_send_at = now()
WHERE id = $1 AND workspace_id = $2
  AND (min_interval_seconds <= 0 OR last_send_at IS NULL
       OR last_send_at <= now() - make_interval(secs => min_interval_seconds))
RETURNING last_send_at;

-- name: DeleteMailbox :execrows
DELETE FROM mailboxes WHERE id = $1 AND workspace_id = $2;

-- name: MailboxExists :one
SELECT EXISTS (SELECT 1 FROM mailboxes WHERE id = $1 AND status = 'active');

-- name: ListActiveMailboxes :many
-- Mailboxes eligible for inbox polling (reply/bounce detection). The poller
-- iterates these and calls GetMailbox per id to get IMAP config + cursor.
--
-- inbox_poll_retry_after is the poll backoff, and this is the ONLY query that
-- reads it. A mailbox whose server is unreachable is skipped for this tick
-- rather than dialed again; it is still 'active', so MailboxExists still says
-- yes and it still SENDS. The backoff is capped (see
-- internal/worker/inbox.DefaultPollBackoff), so the worst case is a mailbox
-- polled hourly instead of every three minutes — it recovers on its own the
-- first time the server answers.
SELECT id, workspace_id FROM mailboxes
WHERE status = 'active'
  AND (inbox_poll_retry_after IS NULL OR inbox_poll_retry_after <= now());

-- name: SetInboxCursor :exec
-- Persists the IMAP poll cursor after a poll pass, so the next pass resumes
-- from inbox_last_seen_uid (or resyncs from scratch if inbox_uid_validity
-- has changed underneath it — an IMAP server-side UIDVALIDITY bump).
--
-- last_poll_at is stamped here too, matching SetInboxCursorString below. It was
-- missing, so an IMAP/SMTP mailbox reported last_poll_at = NULL forever however
-- many times it polled successfully, while Gmail and M365 mailboxes stamped it
-- correctly. That made "never polled" indistinguishable from "polling fine" for
-- exactly the transport with no provider dashboard to check instead.
--
-- The poll backoff is cleared HERE rather than by a second call, for the same
-- reason last_poll_at is stamped here: reaching this statement IS the proof the
-- mailbox answered, and a separate "clear the failures" round trip could fail
-- on its own and leave a healthy mailbox parked on the cap.
UPDATE mailboxes SET inbox_last_seen_uid = $3, inbox_uid_validity = $4, last_poll_at = now(),
    inbox_poll_failures = 0, inbox_poll_retry_after = NULL
WHERE id = $1 AND workspace_id = $2;

-- name: UpdateMailboxSecret :exec
-- Overwrites the sealed credential. Used by the coreapi token-refresh path when
-- an OAuth access/refresh token is rotated, so the new token is persisted.
UPDATE mailboxes SET secret_ciphertext = $3
WHERE id = $1 AND workspace_id = $2;

-- name: SetInboxCursorString :exec
-- Persists an opaque provider cursor (Gmail historyId) after a poll pass, and
-- clears the poll backoff for the reason SetInboxCursor gives above.
UPDATE mailboxes SET inbox_cursor = $3, last_poll_at = now(),
    inbox_poll_failures = 0, inbox_poll_retry_after = NULL
WHERE id = $1 AND workspace_id = $2;

-- name: RecordInboxPollFailure :one
-- Records one failed poll and schedules the next attempt, atomically.
--
-- @backoff_seconds is the LADDER, passed in as data: rung N is the delay after
-- the Nth consecutive failure, and the last rung is the cap (the clamp below is
-- what makes it one). The schedule therefore lives in Go — see
-- internal/worker/inbox.DefaultPollBackoff — and this statement knows only how
-- to index it. A one-rung ladder is how the caller says "this failure cannot
-- fix itself, go straight to the cap".
--
-- The delay is added to the DATABASE clock, not the worker's, because
-- ListActiveMailboxes compares it against the database clock; a few ms of
-- host skew would otherwise decide whether a mailbox is due.
--
-- Old-value semantics: inside SET, `inbox_poll_failures` is the value BEFORE
-- this statement, so `inbox_poll_failures + 1` is the new count and the 1-based
-- ladder index at the same time.
--
-- Not gated on status: a mailbox paused mid-outage still records the failure it
-- just had. It is not polled while paused (ListActiveMailboxes filters on
-- status), and resuming clears the counter anyway (UpdateMailboxStatus).
UPDATE mailboxes
SET inbox_poll_failures = inbox_poll_failures + 1,
    inbox_poll_retry_after = now() + make_interval(secs =>
        (@backoff_seconds::double precision[])[
            LEAST(inbox_poll_failures + 1, array_length(@backoff_seconds::double precision[], 1))
        ])
WHERE id = @id AND workspace_id = @workspace_id
RETURNING inbox_poll_failures, inbox_poll_retry_after;
