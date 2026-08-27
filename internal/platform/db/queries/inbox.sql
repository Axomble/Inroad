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
--
-- mailbox_id is DERIVED here, from the thread, rather than accepted as a
-- parameter. It is denormalized onto this table so CountSentToday's daily-cap
-- half can range-seek (mailbox_id, occurred_at) instead of walking every thread
-- the mailbox ever had — see the migration and queries/send.sql. Deriving it in
-- the statement is what makes that safe: a denormalized column some writer
-- forgets is WORSE than no column, because the count silently under-reports and
-- the daily cap silently over-sends. A writer cannot forget a value it does not
-- supply, and RecordOutboundReply's call site (internal/coreapi/inprocess/
-- inboxreply.go) has only a thread id in hand anyway, so a parameter would have
-- meant plumbing the mailbox across the coreapi seam for no gain.
--
-- The SELECT is pinned on BOTH id and workspace_id, so a thread id from another
-- tenant yields no row and the INSERT fails on the NOT NULL rather than
-- silently attributing a message to a foreign mailbox. That is belt-and-braces:
-- both call sites already set workspace_id from the thread they just read or
-- upserted in the same transaction.
INSERT INTO inbox_messages (thread_id, workspace_id, mailbox_id, direction, message_id, from_email, from_name, to_email, subject, body_text, body_html, reply_class, occurred_at)
SELECT @thread_id, @workspace_id,
       (SELECT t.mailbox_id FROM inbox_threads t WHERE t.id = @thread_id AND t.workspace_id = @workspace_id),
       @direction, @message_id, @from_email, @from_name, @to_email, @subject, @body_text, @body_html, @reply_class, @occurred_at
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
  -- Snooze partitions the inbox in two, so it is a THREE-state filter rather
  -- than another boolean: every ordinary scope hides threads still snoozed
  -- (snooze_hidden), the `snoozed` scope shows only those (snoozed_only), and
  -- neither is set when a caller genuinely wants both (search, which should
  -- find a snoozed thread — hiding it would look like data loss).
  --
  -- "Still snoozed" is snooze_until > now(), evaluated here at read time: an
  -- expired snooze needs no sweeper to bring its thread back, it simply stops
  -- matching. Both arms use the same EXISTS shape so the partial index on
  -- (workspace_id, snooze_until) serves either direction.
  AND (NOT @snooze_hidden::boolean OR NOT EXISTS (
        SELECT 1 FROM inbox_thread_snoozes s
        WHERE s.thread_id = t.id AND s.workspace_id = t.workspace_id
          AND s.snooze_until > now()))
  AND (NOT @snoozed_only::boolean OR EXISTS (
        SELECT 1 FROM inbox_thread_snoozes s
        WHERE s.thread_id = t.id AND s.workspace_id = t.workspace_id
          AND s.snooze_until > now()))
  -- The operator's own label filter. EXISTS rather than a JOIN so a thread
  -- carrying the label twice could never duplicate the row (the composite PK
  -- already prevents that, but a JOIN would make the query's correctness
  -- depend on that constraint rather than on its own shape).
  AND (sqlc.narg('label_id')::uuid IS NULL OR EXISTS (
        SELECT 1 FROM inbox_thread_labels tl
        WHERE tl.thread_id = t.id AND tl.workspace_id = t.workspace_id
          AND tl.label_id = sqlc.narg('label_id')::uuid))
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
WHERE t.workspace_id = @workspace_id
  -- Snoozed threads are excluded from every counter here, because they are
  -- excluded from every list those counters label. The `snoozed` scope has its
  -- own counter (CountInboxSnoozedThreads) rather than a FILTER on this scan:
  -- it is the one count that wants the rows this WHERE removes.
  AND NOT EXISTS (
    SELECT 1 FROM inbox_thread_snoozes s
    WHERE s.thread_id = t.id AND s.workspace_id = t.workspace_id
      AND s.snooze_until > now());

-- name: ListInboxOverviewByMailbox :many
-- Per-mailbox totals for the rail, one row per mailbox that actually has a
-- thread. A mailbox with no threads is absent rather than reported as zero:
-- the rail renders one row per CONNECTED mailbox (which it already fetches
-- from /mailboxes) and looks its count up here, so an empty mailbox reads as
-- 0 without this query having to know the mailbox list.
SELECT t.mailbox_id,
       COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE t.unread)::bigint AS unread
FROM inbox_threads t
WHERE t.workspace_id = @workspace_id
  -- Same snooze exclusion as GetInboxOverviewTotals, for the same reason: a
  -- mailbox's count must match the list clicking that mailbox produces.
  AND NOT EXISTS (
    SELECT 1 FROM inbox_thread_snoozes s
    WHERE s.thread_id = t.id AND s.workspace_id = t.workspace_id
      AND s.snooze_until > now())
GROUP BY t.mailbox_id;

-- name: ListInboxOverviewByReplyClass :many
-- Per-reply-class totals, for the rail's label scopes. Threads whose
-- last_reply_class is '' (never classified) are excluded — '' is the absence
-- of a class, not a class to offer as a filter.
SELECT t.last_reply_class AS key,
       COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE t.unread)::bigint AS unread
FROM inbox_threads t
WHERE t.workspace_id = @workspace_id AND t.last_reply_class <> ''
  -- Same snooze exclusion as GetInboxOverviewTotals: a label's count must
  -- match the list clicking that label produces.
  AND NOT EXISTS (
    SELECT 1 FROM inbox_thread_snoozes s
    WHERE s.thread_id = t.id AND s.workspace_id = t.workspace_id
      AND s.snooze_until > now())
GROUP BY t.last_reply_class;

-- name: UpsertInboxThreadSnooze :one
-- Snooze a thread, or re-snooze one already snoozed (a second call replaces
-- the moment rather than failing — re-snoozing is a normal operator action,
-- not an error). thread_id is the PK, so the conflict target is the whole
-- identity of a snooze.
INSERT INTO inbox_thread_snoozes (thread_id, workspace_id, snooze_until, snoozed_by)
VALUES (@thread_id, @workspace_id, @snooze_until, sqlc.narg('snoozed_by'))
ON CONFLICT (thread_id) DO UPDATE
    SET snooze_until = EXCLUDED.snooze_until,
        snoozed_by   = EXCLUDED.snoozed_by
    -- Belt and braces: the WHERE on the UPDATE arm means a row belonging to
    -- another workspace cannot be overwritten even if a caller somehow reached
    -- this with a foreign thread_id (the service checks the thread first, so
    -- this should be unreachable).
    WHERE inbox_thread_snoozes.workspace_id = @workspace_id
RETURNING *;

-- name: DeleteInboxThreadSnooze :execrows
-- Un-snooze. :execrows so the store can tell "there was a snooze and it is
-- gone" from "there was nothing to remove" and report ErrNotFound for the
-- latter, rather than silently succeeding.
DELETE FROM inbox_thread_snoozes
WHERE thread_id = @thread_id AND workspace_id = @workspace_id;

-- name: GetInboxThreadSnooze :one
SELECT * FROM inbox_thread_snoozes
WHERE thread_id = @thread_id AND workspace_id = @workspace_id;

-- name: CountInboxSnoozedThreads :one
-- The rail's `snoozed` counter: only snoozes still in force. An expired row is
-- not "a snoozed thread" — it is a thread that has already come back, and it
-- is counted by whichever ordinary scope it now falls into.
SELECT COUNT(*)::bigint AS total
FROM inbox_thread_snoozes
WHERE workspace_id = @workspace_id AND snooze_until > now();

-- name: CreateInboxLabel :one
INSERT INTO inbox_labels (workspace_id, name, color)
VALUES (@workspace_id, @name, @color)
RETURNING *;

-- name: ListInboxLabels :many
-- Alphabetical, case-insensitively: the picker is scanned by eye, so "Zebra"
-- must not sort before "apple" the way a raw byte ordering would put it.
SELECT * FROM inbox_labels
WHERE workspace_id = @workspace_id
ORDER BY lower(name);

-- name: GetInboxLabel :one
SELECT * FROM inbox_labels WHERE id = @id AND workspace_id = @workspace_id;

-- name: FindInboxLabelByName :one
-- Backs the picker's search-or-create: a member typing an existing name must
-- get that label rather than a unique-violation. Matched case-insensitively,
-- against the same lower(name) expression the unique index uses.
SELECT * FROM inbox_labels
WHERE workspace_id = @workspace_id AND lower(name) = lower(@name);

-- name: UpdateInboxLabel :one
UPDATE inbox_labels
SET name = @name, color = @color, updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id
RETURNING *;

-- name: DeleteInboxLabel :execrows
-- The join rows go with it via ON DELETE CASCADE: deleting a label unfiles
-- every thread it was on, which is what "delete this label" means. :execrows
-- so an unknown or cross-workspace id becomes ErrNotFound rather than silent
-- success.
DELETE FROM inbox_labels WHERE id = @id AND workspace_id = @workspace_id;

-- name: AssignInboxThreadLabel :exec
-- Idempotent by construction: the composite PK means re-applying an
-- already-applied label is a no-op, not an error the caller must distinguish.
INSERT INTO inbox_thread_labels (thread_id, label_id, workspace_id)
VALUES (@thread_id, @label_id, @workspace_id)
ON CONFLICT (thread_id, label_id) DO NOTHING;

-- name: UnassignInboxThreadLabel :execrows
DELETE FROM inbox_thread_labels
WHERE thread_id = @thread_id AND label_id = @label_id AND workspace_id = @workspace_id;

-- name: ListLabelsForInboxThread :many
SELECT l.* FROM inbox_labels l
JOIN inbox_thread_labels tl ON tl.label_id = l.id AND tl.workspace_id = l.workspace_id
WHERE tl.thread_id = @thread_id AND tl.workspace_id = @workspace_id
ORDER BY lower(l.name);

-- name: ListLabelsForInboxThreads :many
-- The list view's labels, for a whole page of threads in ONE query rather than
-- one per row. Returns (thread_id, label) pairs for the Go layer to group;
-- an array_agg of composite rows would land in sqlc as interface{}.
SELECT tl.thread_id, l.* FROM inbox_labels l
JOIN inbox_thread_labels tl ON tl.label_id = l.id AND tl.workspace_id = l.workspace_id
WHERE tl.workspace_id = @workspace_id AND tl.thread_id = ANY(@thread_ids::uuid[])
ORDER BY tl.thread_id, lower(l.name);

-- name: CountInboxThreadsByLabel :many
-- The rail's per-label counters. Snoozed threads are excluded for the same
-- reason every other counter excludes them: they are absent from the list the
-- counter labels. A label with no (visible) threads is absent rather than
-- reported as zero — the picker renders every label and looks its count up.
SELECT tl.label_id, COUNT(*)::bigint AS total,
       COUNT(*) FILTER (WHERE t.unread)::bigint AS unread
FROM inbox_thread_labels tl
JOIN inbox_threads t ON t.id = tl.thread_id AND t.workspace_id = tl.workspace_id
WHERE tl.workspace_id = @workspace_id
  AND NOT EXISTS (
    SELECT 1 FROM inbox_thread_snoozes s
    WHERE s.thread_id = t.id AND s.workspace_id = t.workspace_id
      AND s.snooze_until > now())
GROUP BY tl.label_id;

-- name: CreateInboxPendingReply :one
-- Self-enforcing tenancy, the same shape ClaimWarmupSend uses: the thread_id is
-- taken from a SELECT over inbox_threads pinned to the workspace, so a
-- cross-tenant thread_id inserts ZERO rows rather than writing a row that
-- merely fails a later check.
INSERT INTO inbox_pending_replies (workspace_id, thread_id, body_text, send_after, created_by)
SELECT t.workspace_id, t.id, @body_text, @send_after, sqlc.narg('created_by')::uuid
FROM inbox_threads t
WHERE t.id = @thread_id AND t.workspace_id = @workspace_id
RETURNING *;

-- name: GetInboxPendingReply :one
SELECT * FROM inbox_pending_replies WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListInboxPendingReplies :many
-- The outbox: everything still waiting, soonest first. Bounded by the caller.
SELECT p.*, t.subject AS thread_subject,
       COALESCE(c.email, '') AS contact_email
FROM inbox_pending_replies p
JOIN inbox_threads t ON t.id = p.thread_id AND t.workspace_id = p.workspace_id
LEFT JOIN contacts c ON c.id = t.contact_id AND c.workspace_id = t.workspace_id
WHERE p.workspace_id = @workspace_id AND p.status IN ('scheduled', 'sending')
ORDER BY p.send_after, p.id
LIMIT @page_limit;

-- name: CountInboxPendingReplies :one
SELECT COUNT(*)::bigint AS total
FROM inbox_pending_replies
WHERE workspace_id = @workspace_id AND status IN ('scheduled', 'sending');

-- name: GetPendingReplyForInboxThread :one
-- The reader's "a reply is in flight" state. At most one row is expected in
-- practice (the UI does not offer a second Send while one is pending), but the
-- schema does not forbid two, so this takes the soonest rather than assuming
-- uniqueness.
SELECT * FROM inbox_pending_replies
WHERE thread_id = @thread_id AND workspace_id = @workspace_id
  AND status IN ('scheduled', 'sending')
ORDER BY send_after
LIMIT 1;

-- name: CancelInboxPendingReply :execrows
-- Undo. Guarded on status='scheduled': a row already claimed by a worker
-- ('sending') is past the point of no return — the SMTP conversation may
-- already be open — so cancelling it would report a lie to the operator. The
-- :execrows result is how the caller learns which happened.
UPDATE inbox_pending_replies
SET status = 'cancelled', updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status = 'scheduled';

-- name: ClaimInboxPendingReply :execrows
-- Claim-before-send, mirroring ClaimWarmupSend. Two rows are claimable: one
-- still 'scheduled' whose send_after has arrived, and one stuck in 'sending'
-- past its lease (a worker that crashed mid-send). The send_after guard is what
-- makes a task that fires early — asynq delivering ahead of schedule, or a
-- retry of an earlier attempt — wait rather than send.
UPDATE inbox_pending_replies
SET status = 'sending', claimed_at = now(), updated_at = now(), last_error = ''
WHERE id = @id AND workspace_id = @workspace_id
  AND send_after <= now()
  AND (status = 'scheduled'
       OR (status = 'sending' AND claimed_at < now() - make_interval(secs => @lease_seconds::double precision)));

-- name: MarkInboxPendingReplySent :execrows
-- Guarded on 'sending' so only the claimer can complete it, and :execrows so
-- the caller runs its side effects (recording the outbound message) exactly
-- once.
UPDATE inbox_pending_replies
SET status = 'sent', message_id = @message_id, sent_at = now(), updated_at = now(), last_error = ''
WHERE id = @id AND workspace_id = @workspace_id AND status = 'sending';

-- name: ReleaseInboxPendingReply :exec
-- A retryable failure: back to 'scheduled' for the next attempt. send_after is
-- deliberately NOT advanced — the reply is already late, and pushing it further
-- out would compound the delay the failure caused.
UPDATE inbox_pending_replies
SET status = 'scheduled', claimed_at = NULL, last_error = @last_error, updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status = 'sending';

-- name: FailInboxPendingReply :exec
-- Terminal failure, after retries are exhausted or on an error retrying cannot
-- fix. The row survives so the outbox can show what happened rather than the
-- reply vanishing.
UPDATE inbox_pending_replies
SET status = 'failed', claimed_at = NULL, last_error = @last_error, updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status IN ('scheduled', 'sending');

-- name: GetWorkspaceInboxSettings :one
SELECT * FROM workspace_inbox_settings WHERE workspace_id = @workspace_id;

-- name: UpsertWorkspaceInboxSettings :one
INSERT INTO workspace_inbox_settings (workspace_id, undo_send_seconds)
VALUES (@workspace_id, @undo_send_seconds)
ON CONFLICT (workspace_id) DO UPDATE
    SET undo_send_seconds = EXCLUDED.undo_send_seconds, updated_at = now()
RETURNING *;

-- name: UpsertInboxComposeDraft :one
-- Autosave. The draft id comes from the client (a UUID it mints when the
-- composer opens), so the first save inserts and every later one updates the
-- same row — no "have I saved yet?" state for the composer to track.
--
-- The WHERE on the UPDATE arm pins BOTH workspace and user: a draft is
-- personal, so even a colleague in the same workspace cannot overwrite it.
INSERT INTO inbox_compose_drafts (id, workspace_id, user_id, mailbox_id, to_emails, cc_emails, bcc_emails, subject, body_text)
VALUES (@id, @workspace_id, @user_id, sqlc.narg('mailbox_id')::uuid, @to_emails, @cc_emails, @bcc_emails, @subject, @body_text)
ON CONFLICT (id) DO UPDATE
    SET mailbox_id = EXCLUDED.mailbox_id,
        to_emails  = EXCLUDED.to_emails,
        cc_emails  = EXCLUDED.cc_emails,
        bcc_emails = EXCLUDED.bcc_emails,
        subject    = EXCLUDED.subject,
        body_text  = EXCLUDED.body_text,
        updated_at = now()
    WHERE inbox_compose_drafts.workspace_id = @workspace_id
      AND inbox_compose_drafts.user_id = @user_id
RETURNING *;

-- name: ListInboxComposeDrafts :many
-- This user's own drafts only. Most recently edited first — the one they are
-- most likely to want back.
SELECT * FROM inbox_compose_drafts
WHERE workspace_id = @workspace_id AND user_id = @user_id
ORDER BY updated_at DESC
LIMIT @page_limit;

-- name: GetInboxComposeDraft :one
SELECT * FROM inbox_compose_drafts
WHERE id = @id AND workspace_id = @workspace_id AND user_id = @user_id;

-- name: DeleteInboxComposeDraft :execrows
DELETE FROM inbox_compose_drafts
WHERE id = @id AND workspace_id = @workspace_id AND user_id = @user_id;

-- name: CreateInboxPendingCompose :one
-- Self-enforcing tenancy on the MAILBOX, the same shape
-- CreateInboxPendingReply uses on the thread: the mailbox_id is taken from a
-- SELECT pinned to the workspace, so a cross-tenant mailbox inserts zero rows
-- rather than a row that would send through another workspace's credentials.
INSERT INTO inbox_pending_composes
    (workspace_id, mailbox_id, to_emails, cc_emails, bcc_emails, subject, body_text, send_after, created_by)
SELECT m.workspace_id, m.id, @to_emails, @cc_emails, @bcc_emails, @subject, @body_text, @send_after,
       sqlc.narg('created_by')::uuid
FROM mailboxes m
WHERE m.id = @mailbox_id AND m.workspace_id = @workspace_id
RETURNING *;

-- name: GetInboxPendingCompose :one
SELECT * FROM inbox_pending_composes WHERE id = @id AND workspace_id = @workspace_id;

-- name: ListInboxPendingComposes :many
SELECT c.*, COALESCE(m.email, '') AS mailbox_email
FROM inbox_pending_composes c
LEFT JOIN mailboxes m ON m.id = c.mailbox_id AND m.workspace_id = c.workspace_id
WHERE c.workspace_id = @workspace_id AND c.status IN ('scheduled', 'sending')
ORDER BY c.send_after, c.id
LIMIT @page_limit;

-- name: CancelInboxPendingCompose :execrows
-- Guarded on 'scheduled', exactly as the reply path is: a claimed row may
-- already have an open SMTP conversation.
UPDATE inbox_pending_composes
SET status = 'cancelled', updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status = 'scheduled';

-- name: ClaimInboxPendingCompose :execrows
UPDATE inbox_pending_composes
SET status = 'sending', claimed_at = now(), updated_at = now(), last_error = ''
WHERE id = @id AND workspace_id = @workspace_id
  AND send_after <= now()
  AND (status = 'scheduled'
       OR (status = 'sending' AND claimed_at < now() - make_interval(secs => @lease_seconds::double precision)));

-- name: MarkInboxPendingComposeSent :execrows
UPDATE inbox_pending_composes
SET status = 'sent', message_id = @message_id, sent_at = now(), updated_at = now(), last_error = ''
WHERE id = @id AND workspace_id = @workspace_id AND status = 'sending';

-- name: ReleaseInboxPendingCompose :exec
UPDATE inbox_pending_composes
SET status = 'scheduled', claimed_at = NULL, last_error = @last_error, updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status = 'sending';

-- name: FailInboxPendingCompose :exec
UPDATE inbox_pending_composes
SET status = 'failed', claimed_at = NULL, last_error = @last_error, updated_at = now()
WHERE id = @id AND workspace_id = @workspace_id AND status IN ('scheduled', 'sending');
