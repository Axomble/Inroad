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
-- legacy direct-send match) has nothing to join on and reports an empty
-- string via COALESCE, never NULL — the same "absent is empty string"
-- convention inbox_messages' own text columns already use. The query
-- parameter substring-matches (case-insensitive) the thread's subject OR the
-- joined contact's email; the caller escapes LIKE metacharacters before this
-- reaches Postgres (see db.EscapeLike in platform/db) so a literal % or _ typed
-- by a user is never treated as a wildcard.
--
-- reply_label_label/reply_label_color are a SECOND LEFT JOIN, on
-- (workspace_id, key), resolving last_reply_class to its current display
-- label — nullable columns (not COALESCE'd) so the Go layer can tell "no
-- label matches this key" (a deleted custom label, or a core that predates
-- the taxonomy) apart from "the key IS the empty string" and degrade to the
-- raw key, per the OpenAPI contract's InboxThreadSummary.reply_label doc.
SELECT t.*,
       COALESCE(c.email, '') AS contact_email,
       COALESCE(c.first_name, '') AS contact_first_name,
       COALESCE(c.last_name, '') AS contact_last_name,
       rl.label AS reply_label_label,
       rl.color AS reply_label_color
FROM inbox_threads t
LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
LEFT JOIN reply_labels rl ON rl.workspace_id = t.workspace_id AND rl.key = t.last_reply_class
WHERE t.workspace_id = @workspace_id
  AND (sqlc.narg('mailbox_id')::uuid IS NULL OR t.mailbox_id = sqlc.narg('mailbox_id'))
  AND (sqlc.narg('reply_class')::text IS NULL OR t.last_reply_class = sqlc.narg('reply_class'))
  AND (sqlc.narg('before_last_message_at')::timestamptz IS NULL
       OR (t.last_message_at, t.id) < (sqlc.narg('before_last_message_at')::timestamptz, sqlc.narg('before_id')::uuid))
  AND (sqlc.narg('query')::text IS NULL
       OR t.subject ILIKE '%' || sqlc.narg('query')::text || '%'
       OR c.email ILIKE '%' || sqlc.narg('query')::text || '%')
  -- The rail's virtual scopes. Each is written so an unscoped caller's
  -- second operand is never EVALUATED per row (`NOT false` is TRUE, and
  -- `TRUE OR x` needs no x), which is what keeps the awaiting-reply
  -- subqueries off the unscoped path. The RESULT SET for a caller passing
  -- none of these is provably identical to the query before they existed;
  -- the plan may still mention the sublinks, since Postgres does not define
  -- OR as short-circuiting — run EXPLAIN, don't take this comment's word.
  AND (NOT @unread_only::boolean OR t.unread)
  AND (sqlc.narg('since_last_message_at')::timestamptz IS NULL
       OR t.last_message_at >= sqlc.narg('since_last_message_at')::timestamptz)
  -- "Waiting on us" — the same rule the rail's counter uses, shared as one
  -- function so a count can never disagree with the list it links to. See
  -- inbox_thread_awaiting_reply's own comment for why this must span both the
  -- stored and the synthesized outbound legs.
  AND (NOT @awaiting_reply_only::boolean
       OR inbox_thread_awaiting_reply(t.id, t.workspace_id, t.campaign_id, t.contact_id))
ORDER BY t.last_message_at DESC, t.id DESC
LIMIT @page_limit;

-- name: GetInboxThread :one
-- contact_* and reply_label_* columns follow ListInboxThreads' LEFT JOIN
-- conventions above.
SELECT t.*,
       COALESCE(c.email, '') AS contact_email,
       COALESCE(c.first_name, '') AS contact_first_name,
       COALESCE(c.last_name, '') AS contact_last_name,
       rl.label AS reply_label_label,
       rl.color AS reply_label_color
FROM inbox_threads t
LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
LEFT JOIN reply_labels rl ON rl.workspace_id = t.workspace_id AND rl.key = t.last_reply_class
WHERE t.id = @id AND t.workspace_id = @workspace_id;

-- name: ListInboxMessagesByThread :many
SELECT * FROM inbox_messages WHERE thread_id = @thread_id AND workspace_id = @workspace_id ORDER BY occurred_at;

-- name: BumpInboxThreadLastMessageAt :exec
-- Advances a thread's last_message_at after an operator's manual reply is
-- recorded (Service.RecordOutboundReply), WITHOUT touching unread or
-- last_reply_class: unread was already cleared by Service.Reply when it
-- enqueued the send (POST /inbox/threads/{id}/reply marks the thread read),
-- and an operator's own outbound reply is not itself a classified inbound
-- reply. Mirrors UpsertInboxThread's bump but deliberately narrower.
UPDATE inbox_threads SET last_message_at = now() WHERE id = @id AND workspace_id = @workspace_id;

-- name: SetInboxThreadUnread :execrows
-- :execrows (not :exec) so the store can tell "matched and updated" apart
-- from "zero rows matched" (an unknown or cross-workspace id) and map the
-- latter to ErrNotFound, rather than silently reporting success.
UPDATE inbox_threads SET unread = @unread WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListSentOutboundStepsForThread :many
-- The outbound leg of a thread (design spec: "Data model" — the original
-- sent message + any follow-up steps already sent), synthesized at READ time
-- by joining sends to the step that sent it (by campaign_id + step_order),
-- never duplicated into inbox_messages since the send content already lives
-- in sequence_steps. Only steps that actually went out are returned
-- (sent_at IS NOT NULL) — a queued/failed/skipped step never happened and
-- must not appear in a reply thread. from_email/from_name come from the
-- sending mailbox; a LEFT JOIN (COALESCE to '') rather than an INNER JOIN
-- purely as defense in depth — sends.mailbox_id is NOT NULL and FK's
-- ON DELETE CASCADE to mailboxes, so in practice the row always exists.
SELECT s.step_order, s.to_email, s.message_id, s.sent_at, s.created_at,
       st.subject AS step_subject, st.body_text AS step_body_text, st.body_html AS step_body_html,
       COALESCE(m.email, '') AS from_email, COALESCE(m.display_name, '') AS from_name
FROM sends s
JOIN sequence_steps st ON st.campaign_id = s.campaign_id AND st.step_order = s.step_order AND st.workspace_id = s.workspace_id
LEFT JOIN mailboxes m ON m.id = s.mailbox_id AND m.workspace_id = s.workspace_id
WHERE s.workspace_id = @workspace_id AND s.campaign_id = @campaign_id AND s.contact_id = @contact_id
  AND s.sent_at IS NOT NULL
ORDER BY s.step_order;

-- name: GetInboxOverviewTotals :one
-- The scope rail's headline counters, computed by Postgres over the whole
-- workspace rather than sampled client-side from one page of threads.
--
-- Every counter is a FILTER aggregate over the SAME single scan, so the rail
-- costs one query rather than one per scope. All five are cast explicitly:
-- COUNT(*) is bigint, but a bare COALESCE/FILTER aggregate makes sqlc emit
-- interface{} while still compiling — the cast is what pins the generated
-- field to int64.
--
-- The window boundaries are computed from a caller-supplied @now rather than
-- Postgres' own now(): "today" is a question about the VIEWER's day, and the
-- server has no business assuming its own timezone is theirs. The caller
-- passes the start of the viewer's today and of their week, already resolved.
--
-- awaiting_reply counts threads where the CONTACT spoke last, so the thread
-- is waiting on us. See the awaiting_reply_only predicate on
-- ListInboxThreads for why this must consider the sends table and not just
-- inbox_messages; the two definitions are deliberately identical, so a count
-- here always matches the list that scope serves.
SELECT
    COUNT(*)::bigint AS total,
    COUNT(*) FILTER (WHERE t.unread)::bigint AS unread,
    COUNT(*) FILTER (WHERE t.last_message_at >= @today_start::timestamptz)::bigint AS today,
    COUNT(*) FILTER (WHERE t.last_message_at >= @week_start::timestamptz)::bigint AS this_week,
    COUNT(*) FILTER (WHERE inbox_thread_awaiting_reply(t.id, t.workspace_id, t.campaign_id, t.contact_id))::bigint AS awaiting_reply
FROM inbox_threads t
WHERE t.workspace_id = @workspace_id;

-- name: ListInboxOverviewByMailbox :many
-- Per-mailbox totals for the rail, one row per mailbox that actually has a
-- thread. A mailbox with no threads is absent rather than reported as zero:
-- the rail renders one row per CONNECTED mailbox (which it already fetches
-- from /mailboxes) and looks its count up here, so an empty mailbox reads as
-- 0 without this query having to know the mailbox list.
SELECT mailbox_id,
       COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE unread)::bigint AS unread
FROM inbox_threads
WHERE workspace_id = @workspace_id
GROUP BY mailbox_id;

-- name: ListInboxOverviewByReplyClass :many
-- Per-reply-class totals, for the rail's label scopes. Threads whose
-- last_reply_class is '' (never classified) are excluded — '' is the absence
-- of a class, not a class to offer as a filter.
SELECT last_reply_class AS key,
       COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE unread)::bigint AS unread
FROM inbox_threads
WHERE workspace_id = @workspace_id AND last_reply_class <> ''
GROUP BY last_reply_class;
