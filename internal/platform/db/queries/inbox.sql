-- name: UpsertInboxThread :one
-- Lazy thread creation: the FIRST reply to a root_message_id creates the row;
-- every later reply just bumps last_reply_class/last_message_at/unread. A
-- legacy match (root_message_id = '') never conflicts (see the partial
-- unique index) and always inserts a fresh row.
INSERT INTO inbox_threads (workspace_id, mailbox_id, campaign_id, contact_id, root_message_id, subject, last_reply_class, last_message_at)
VALUES (@workspace_id, @mailbox_id, sqlc.narg('campaign_id')::uuid, sqlc.narg('contact_id')::uuid, @root_message_id, @subject, @last_reply_class, now())
ON CONFLICT (workspace_id, mailbox_id, root_message_id) WHERE root_message_id <> ''
DO UPDATE SET last_reply_class = @last_reply_class, last_message_at = now(), unread = true
RETURNING *;

-- name: InsertInboxMessage :exec
-- The conflict target names the partial unique index's columns + predicate
-- directly (index inference) rather than `ON CONFLICT ON CONSTRAINT`: a
-- CREATE UNIQUE INDEX ... WHERE ... is an index, not a named constraint (a
-- table constraint cannot carry a WHERE clause), so ON CONSTRAINT cannot
-- target it — Postgres rejects that with "constraint ... does not exist".
INSERT INTO inbox_messages (thread_id, workspace_id, direction, message_id, from_email, from_name, to_email, subject, body_text, body_html, reply_class, occurred_at)
VALUES (@thread_id, @workspace_id, @direction, @message_id, @from_email, @from_name, @to_email, @subject, @body_text, @body_html, @reply_class, @occurred_at)
ON CONFLICT (workspace_id, message_id) WHERE message_id <> '' DO NOTHING;

-- name: ListInboxThreads :many
-- Keyset on (last_message_at, id) DESC, newest first. sqlc.narg fields are
-- optional filters, omitted (NULL) when the caller doesn't set them.
SELECT * FROM inbox_threads
WHERE workspace_id = @workspace_id
  AND (sqlc.narg('mailbox_id')::uuid IS NULL OR mailbox_id = sqlc.narg('mailbox_id'))
  AND (sqlc.narg('reply_class')::text IS NULL OR last_reply_class = sqlc.narg('reply_class'))
  AND (sqlc.narg('before_last_message_at')::timestamptz IS NULL
       OR (last_message_at, id) < (sqlc.narg('before_last_message_at')::timestamptz, sqlc.narg('before_id')::uuid))
ORDER BY last_message_at DESC, id DESC
LIMIT @page_limit;

-- name: GetInboxThread :one
SELECT * FROM inbox_threads WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListInboxMessagesByThread :many
SELECT * FROM inbox_messages WHERE thread_id = @thread_id AND workspace_id = @workspace_id ORDER BY occurred_at;

-- name: SetInboxThreadUnread :exec
UPDATE inbox_threads SET unread = @unread WHERE id = @id AND workspace_id = @workspace_id;
