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
-- contact_* columns come from a LEFT JOIN: a thread with no contact_id (a
-- legacy direct-send match) has nothing to join on and reports '' via
-- COALESCE, never NULL — the same "absent is empty string" convention
-- inbox_messages' own text columns already use. 'query' substring-matches
-- (case-insensitive) the thread's subject OR the joined contact's email; the
-- caller escapes LIKE metacharacters before this reaches Postgres (see
-- escapeLike in store.go) so a literal % or _ typed by a user is never
-- treated as a wildcard.
SELECT t.*,
       COALESCE(c.email, '') AS contact_email,
       COALESCE(c.first_name, '') AS contact_first_name,
       COALESCE(c.last_name, '') AS contact_last_name
FROM inbox_threads t
LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
WHERE t.workspace_id = @workspace_id
  AND (sqlc.narg('mailbox_id')::uuid IS NULL OR t.mailbox_id = sqlc.narg('mailbox_id'))
  AND (sqlc.narg('reply_class')::text IS NULL OR t.last_reply_class = sqlc.narg('reply_class'))
  AND (sqlc.narg('before_last_message_at')::timestamptz IS NULL
       OR (t.last_message_at, t.id) < (sqlc.narg('before_last_message_at')::timestamptz, sqlc.narg('before_id')::uuid))
  AND (sqlc.narg('query')::text IS NULL
       OR t.subject ILIKE '%' || sqlc.narg('query')::text || '%'
       OR c.email ILIKE '%' || sqlc.narg('query')::text || '%')
ORDER BY t.last_message_at DESC, t.id DESC
LIMIT @page_limit;

-- name: GetInboxThread :one
-- contact_* columns follow ListInboxThreads' LEFT JOIN convention above.
SELECT t.*,
       COALESCE(c.email, '') AS contact_email,
       COALESCE(c.first_name, '') AS contact_first_name,
       COALESCE(c.last_name, '') AS contact_last_name
FROM inbox_threads t
LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
WHERE t.id = @id AND t.workspace_id = @workspace_id;

-- name: ListInboxMessagesByThread :many
SELECT * FROM inbox_messages WHERE thread_id = @thread_id AND workspace_id = @workspace_id ORDER BY occurred_at;

-- name: SetInboxThreadUnread :exec
UPDATE inbox_threads SET unread = @unread WHERE id = @id AND workspace_id = @workspace_id;
