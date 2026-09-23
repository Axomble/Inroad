-- name: SearchInboxThreads :many
-- Full-text search over a workspace's threads, covering BOTH legs: stored
-- messages (inbound replies + manual outbound replies, in inbox_messages) and
-- the campaign sends synthesized into each thread from sends -> sequence_steps
-- / sequence_step_variants (see ListSentOutboundStepsForThread). The document
-- and query definitions are the SQL functions from
-- 20260923105701_inbox_full_text_search; every text predicate below calls
-- inbox_search_document with the exact arguments its index was built on, which
-- is what lets the planner use the index at all.
--
-- SHAPE. The expensive part — matching text — runs ONCE per search, through
-- the GIN indexes, into small sets: message_hits (threads with a matching
-- stored message) and step_hits (campaign content that matches). The thread
-- walk then only does cheap lookups against those sets, newest first, and
-- stops at the page limit. ts_headline runs only on the rows of the returned
-- page (the outer query), never on a candidate that is filtered out or falls
-- past the limit.
--
-- ORDER is last_message_at DESC, id DESC — the inbox's own order and keyset —
-- not relevance. Rank-ordered keyset pagination is unstable under a ts_rank
-- that ties and floats, and a mail search reads naturally newest-first.
--
-- Filters mirror ListInboxThreads exactly (see its comments for the reasoning
-- behind each); a search inside a scope must return a subset of that scope.
--
-- The tsquery is written inline as inbox_search_query(@query) at every use
-- rather than hoisted into a CTE: an immutable function of a parameter is a
-- plan-time constant the GIN index can be probed with directly, whereas a CTE
-- column is a join input the planner would have to thread through a
-- parameterized nested loop to reach the index.
WITH message_hits AS MATERIALIZED (
    SELECT m.thread_id,
           bool_or(m.direction = 'inbound') AS inbound,
           bool_or(m.direction = 'outbound') AS outbound
    FROM inbox_messages m
    WHERE m.workspace_id = @workspace_id
      AND inbox_search_document(m.subject, m.body_text) @@ inbox_search_query(@query::text)
    GROUP BY m.thread_id
),
-- The content a campaign send actually carried: the step's own copy when
-- sends.variant_id IS NULL, else that variant's. variant_id is NULL for the
-- base row so the send-side join below can match the two cases with one
-- IS NOT DISTINCT FROM.
step_hits AS MATERIALIZED (
    SELECT st.campaign_id, st.step_order, NULL::uuid AS variant_id
    FROM sequence_steps st
    WHERE st.workspace_id = @workspace_id
      AND inbox_search_document(st.subject, st.body_text) @@ inbox_search_query(@query::text)
    UNION ALL
    SELECT st.campaign_id, st.step_order, v.id AS variant_id
    FROM sequence_step_variants v
    JOIN sequence_steps st ON st.id = v.step_id AND st.workspace_id = v.workspace_id
    WHERE v.workspace_id = @workspace_id
      AND inbox_search_document(v.subject, v.body_text) @@ inbox_search_query(@query::text)
),
page AS (
    SELECT t.id, t.workspace_id, t.mailbox_id, t.campaign_id, t.contact_id,
           t.root_message_id, t.subject, t.last_reply_class, t.unread,
           t.last_message_at, t.created_at,
           COALESCE(mh.inbound, false)::boolean AS matched_inbound,
           (COALESCE(mh.outbound, false) OR campaign_hit.hit IS NOT NULL)::boolean AS matched_outbound
    FROM inbox_threads t
    LEFT JOIN message_hits mh ON mh.thread_id = t.id
    -- A thread's campaign leg matches when one of ITS sends (same campaign and
    -- contact, actually sent) carried matching content. The IN guard is
    -- evaluated first, against the tiny step_hits set, so a thread whose
    -- campaign has no matching content never touches sends at all.
    LEFT JOIN LATERAL (
        SELECT true AS hit
        FROM sends s
        JOIN step_hits h ON h.campaign_id = s.campaign_id
                        AND h.step_order = s.step_order
                        AND h.variant_id IS NOT DISTINCT FROM s.variant_id
        WHERE t.campaign_id IN (SELECT campaign_id FROM step_hits)
          AND s.workspace_id = t.workspace_id
          AND s.campaign_id = t.campaign_id
          AND s.contact_id = t.contact_id
          AND s.sent_at IS NOT NULL
        LIMIT 1
    ) campaign_hit ON true
    WHERE t.workspace_id = @workspace_id
      AND (mh.thread_id IS NOT NULL OR campaign_hit.hit IS NOT NULL)
      AND (sqlc.narg('mailbox_id')::uuid IS NULL OR t.mailbox_id = sqlc.narg('mailbox_id'))
      AND (sqlc.narg('reply_class')::text IS NULL OR t.last_reply_class = sqlc.narg('reply_class'))
      AND (sqlc.narg('after_last_message_at')::timestamptz IS NULL
           OR (t.last_message_at, t.id) < (sqlc.narg('after_last_message_at')::timestamptz, sqlc.narg('after_id')::uuid))
      AND (NOT @unread_only::boolean OR t.unread)
      AND (sqlc.narg('since_last_message_at')::timestamptz IS NULL
           OR t.last_message_at >= sqlc.narg('since_last_message_at')::timestamptz)
      AND (NOT @awaiting_reply_only::boolean
           OR inbox_thread_awaiting_reply(t.id, t.workspace_id, t.campaign_id, t.contact_id))
      AND (NOT @snooze_hidden::boolean OR NOT EXISTS (
            SELECT 1 FROM inbox_thread_snoozes sn
            WHERE sn.thread_id = t.id AND sn.workspace_id = t.workspace_id
              AND sn.snooze_until > now()))
      AND (NOT @snoozed_only::boolean OR EXISTS (
            SELECT 1 FROM inbox_thread_snoozes sn
            WHERE sn.thread_id = t.id AND sn.workspace_id = t.workspace_id
              AND sn.snooze_until > now()))
      AND (sqlc.narg('label_id')::uuid IS NULL OR EXISTS (
            SELECT 1 FROM inbox_thread_labels tl
            WHERE tl.thread_id = t.id AND tl.workspace_id = t.workspace_id
              AND tl.label_id = sqlc.narg('label_id')::uuid))
    ORDER BY t.last_message_at DESC, t.id DESC
    LIMIT @page_limit
)
SELECT p.id, p.workspace_id, p.mailbox_id, p.campaign_id, p.contact_id,
       p.root_message_id, p.subject, p.last_reply_class, p.unread,
       p.last_message_at, p.created_at,
       p.matched_inbound, p.matched_outbound,
       COALESCE(c.email, '') AS contact_email,
       COALESCE(c.first_name, '') AS contact_first_name,
       COALESCE(c.last_name, '') AS contact_last_name,
       rl.label AS reply_label_label,
       rl.color AS reply_label_color,
       COALESCE(best.direction, '')::text AS snippet_direction,
       best.occurred_at::timestamptz AS snippet_occurred_at,
       -- snippet_direction is '' only when no snippet could be re-derived (see
       -- the LEFT JOIN LATERAL below); the text columns are then '' too.
       --
       -- The highlight markers are chosen by the caller (private-use code
       -- points it can split on) and stripped from the INPUT first, so a
       -- message that happens to contain one can never forge a highlight.
       -- ts_headline's 'english' must stay the regconfig inbox_search_document
       -- and inbox_search_query use, or it would stem differently from the
       -- match and fail to highlight the very words that matched.
       -- HighlightAll on the subject keeps it whole; the body is cut to
       -- fragments around the matches. The body is capped for ts_headline
       -- (which re-parses its whole input) at a fifth of the indexed length:
       -- a match deeper than that still finds the thread, it just yields a
       -- lead-of-body snippet without a highlight.
       COALESCE(ts_headline('english'::regconfig,
           replace(replace(best.subject, @highlight_start::text, ''), @highlight_stop::text, ''),
           inbox_search_query(@query::text),
           'HighlightAll=true, StartSel=' || @highlight_start::text || ', StopSel=' || @highlight_stop::text
       ), '')::text AS snippet_subject,
       COALESCE(ts_headline('english'::regconfig,
           replace(replace(left(best.body, 20000), @highlight_start::text, ''), @highlight_stop::text, ''),
           inbox_search_query(@query::text),
           'MaxFragments=2, MaxWords=24, MinWords=8, FragmentDelimiter=" … ", StartSel=' || @highlight_start::text || ', StopSel=' || @highlight_stop::text
       ), '')::text AS snippet_body
FROM page p
LEFT JOIN contacts c ON c.id = p.contact_id AND c.workspace_id = p.workspace_id
LEFT JOIN reply_labels rl ON rl.workspace_id = p.workspace_id AND rl.key = p.last_reply_class
-- The snippet comes from the NEWEST matching message on the thread, across
-- both legs. LEFT (not CROSS) join: the page already decided membership, and a
-- snippet that could not be re-derived must not silently drop a row from it.
LEFT JOIN LATERAL (
    SELECT candidates.direction, candidates.subject, candidates.body, candidates.occurred_at
    FROM (
        SELECT m.direction, m.subject, m.body_text AS body, m.occurred_at
        FROM inbox_messages m
        WHERE m.thread_id = p.id AND m.workspace_id = p.workspace_id
          AND inbox_search_document(m.subject, m.body_text) @@ inbox_search_query(@query::text)
        UNION ALL
        SELECT 'outbound' AS direction,
               COALESCE(v.subject, st.subject) AS subject,
               COALESCE(v.body_text, st.body_text) AS body,
               s.sent_at AS occurred_at
        FROM sends s
        JOIN sequence_steps st ON st.campaign_id = s.campaign_id AND st.step_order = s.step_order AND st.workspace_id = s.workspace_id
        LEFT JOIN sequence_step_variants v ON v.id = s.variant_id AND v.workspace_id = s.workspace_id
        WHERE p.matched_outbound
          AND s.workspace_id = p.workspace_id AND s.campaign_id = p.campaign_id AND s.contact_id = p.contact_id
          AND s.sent_at IS NOT NULL
          AND inbox_search_document(COALESCE(v.subject, st.subject), COALESCE(v.body_text, st.body_text)) @@ inbox_search_query(@query::text)
    ) candidates
    ORDER BY candidates.occurred_at DESC
    LIMIT 1
) best ON true
ORDER BY p.last_message_at DESC, p.id DESC;
