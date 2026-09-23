-- name: SetInboxSearchStatementTimeout :exec
-- Bounds every statement of ONE search transaction. set_config(..., true) is
-- SET LOCAL: it reverts when the transaction ends, so the pooled connection
-- goes back with its normal timeout. A parameterized SET is not possible,
-- which is why this is set_config rather than SET LOCAL.
SELECT set_config('statement_timeout', @timeout::text, true);

-- name: InboxSearchPrecheck :one
-- Decides, before any thread is walked, whether a search is one this endpoint
-- will answer — and does it with work that is itself bounded.
--
-- tree is querytree() of the parsed query: the part of it an index can use.
-- It is 'T' when nothing positive remains (a pure negation like `-zzzz`, or
-- `foo OR -bar`, both of which match nearly every row and would force a scan
-- of the whole GIN index), and '' when the query had no lexemes at all (only
-- stop words or punctuation). The caller rejects both. The candidate counts are
-- evaluated only when the tree is usable — CASE does not evaluate a branch it
-- does not take — so a rejected query never touches an index.
--
-- The counts are capped at @candidate_cap (the caller passes the cap + 1) so
-- that counting can never cost more than the search it is guarding: a count
-- of cap + 1 means "more than the cap", and the caller refuses the query as
-- too broad. It is a SEPARATE statement from the search (rather than a column
-- on its rows) because an over-broad query whose candidates the scope filters
-- all remove would otherwise return an empty page instead of the refusal.
WITH parsed AS (
    SELECT querytree(inbox_search_query(@query::text))::text AS tree
)
SELECT parsed.tree,
       (CASE WHEN parsed.tree IN ('T', '') THEN 0 ELSE (
           SELECT count(*) FROM (
               SELECT 1 FROM inbox_messages m
               WHERE m.workspace_id = @workspace_id
                 AND inbox_search_document(m.subject, m.body_text) @@ inbox_search_query(@query::text)
               LIMIT @candidate_cap
           ) capped_messages)
        END)::bigint AS message_candidates,
       (CASE WHEN parsed.tree IN ('T', '') THEN 0 ELSE (
           SELECT count(*) FROM (
               SELECT 1 FROM sequence_steps st
               WHERE st.workspace_id = @workspace_id
                 AND inbox_search_document(st.subject, st.body_text) @@ inbox_search_query(@query::text)
               UNION ALL
               SELECT 1 FROM sequence_step_variants v
               WHERE v.workspace_id = @workspace_id
                 AND inbox_search_document(v.subject, v.body_text) @@ inbox_search_query(@query::text)
               LIMIT @candidate_cap
           ) capped_steps)
        END)::bigint AS step_candidates,
       -- The address candidates (see SearchInboxThreads' contact_matches and
       -- sender_matches). NULL address_pattern — a query too short for the
       -- trigram index — means address matching is off and counts nothing.
       (CASE WHEN parsed.tree IN ('T', '') OR sqlc.narg('address_pattern')::text IS NULL THEN 0 ELSE (
           SELECT count(*) FROM (
               SELECT 1 FROM contacts c
               WHERE c.workspace_id = @workspace_id
                 AND c.search_text LIKE '%' || sqlc.narg('address_pattern')::text || '%'
                 AND lower(c.email) LIKE '%' || sqlc.narg('address_pattern')::text || '%'
               LIMIT @candidate_cap
           ) capped_contacts)
        END)::bigint AS contact_candidates,
       (CASE WHEN parsed.tree IN ('T', '') OR sqlc.narg('address_pattern')::text IS NULL THEN 0 ELSE (
           SELECT count(*) FROM (
               SELECT 1 FROM inbox_messages m
               WHERE m.workspace_id = @workspace_id AND m.direction = 'inbound'
                 AND lower(m.from_email) LIKE '%' || sqlc.narg('address_pattern')::text || '%'
               LIMIT @candidate_cap
           ) capped_senders)
        END)::bigint AS sender_candidates
FROM parsed;

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
-- BOUNDED BY CONSTRUCTION. Text is matched exactly ONCE per search, through the
-- GIN indexes, into two candidate sets — message_matches (matching stored
-- messages) and step_hits (matching campaign copy) — and addresses into two
-- more (contact_matches, sender_matches), each capped at @candidate_cap rows.
-- Nothing after that recomputes a tsvector or re-runs a LIKE: the thread walk
-- does cheap lookups against the four sets, and the snippet re-reads only rows
-- the sets already named (or, for an address-only hit, one newest message).
-- ts_headline runs only on the rows of the returned page. The caller runs InboxSearchPrecheck first, in the same
-- REPEATABLE READ transaction, and refuses any query whose candidates exceed
-- the cap, so the LIMITs here never actually truncate — they are the backstop
-- that keeps this statement bounded even if a caller skipped the precheck.
-- The whole transaction also runs under a statement_timeout.
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
WITH message_matches AS MATERIALIZED (
    SELECT m.id, m.thread_id, m.direction, m.occurred_at
    FROM inbox_messages m
    WHERE m.workspace_id = @workspace_id
      AND inbox_search_document(m.subject, m.body_text) @@ inbox_search_query(@query::text)
    LIMIT @candidate_cap
),
message_hits AS MATERIALIZED (
    SELECT mm.thread_id,
           bool_or(mm.direction = 'inbound') AS inbound,
           bool_or(mm.direction = 'outbound') AS outbound
    FROM message_matches mm
    GROUP BY mm.thread_id
),
-- The content a campaign send actually carried: the step's own copy when
-- sends.variant_id IS NULL, else that variant's. variant_id is NULL for the
-- base row so the send-side joins below can match the two cases with one
-- IS NOT DISTINCT FROM.
step_hits AS MATERIALIZED (
    SELECT matched.campaign_id, matched.step_order, matched.variant_id
    FROM (
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
    ) matched
    LIMIT @candidate_cap
),
-- ADDRESS MATCHING. The whole query, lower-cased and LIKE-escaped by the
-- caller (address_pattern), as a substring of the linked contact's email or of
-- an inbound message's From address — what the inbox list's old substring
-- search did for the contact's email, so typing "jo@acme" or "acme.com" still
-- finds the thread. NULL when the query is under three characters, the
-- shortest a trigram index can serve; address matching is then off.
--
-- contact_matches goes through idx_contacts_search (search_text is the
-- lower-cased email + name + company projection), and re-checks lower(email)
-- so a name or company that happens to contain the text does not count as an
-- address match. sender_matches goes through
-- idx_inbox_messages_from_email_search. Both are capped like the text sets.
contact_matches AS MATERIALIZED (
    SELECT c.id
    FROM contacts c
    WHERE sqlc.narg('address_pattern')::text IS NOT NULL
      AND c.workspace_id = @workspace_id
      AND c.search_text LIKE '%' || sqlc.narg('address_pattern')::text || '%'
      AND lower(c.email) LIKE '%' || sqlc.narg('address_pattern')::text || '%'
    LIMIT @candidate_cap
),
sender_matches AS MATERIALIZED (
    SELECT m.thread_id
    FROM inbox_messages m
    WHERE sqlc.narg('address_pattern')::text IS NOT NULL
      AND m.workspace_id = @workspace_id AND m.direction = 'inbound'
      AND lower(m.from_email) LIKE '%' || sqlc.narg('address_pattern')::text || '%'
    LIMIT @candidate_cap
),
page AS (
    SELECT t.id, t.workspace_id, t.mailbox_id, t.campaign_id, t.contact_id,
           t.root_message_id, t.subject, t.last_reply_class, t.unread,
           t.last_message_at, t.created_at,
           (mh.thread_id IS NOT NULL)::boolean AS matched_stored,
           (campaign_hit.hit IS NOT NULL)::boolean AS matched_campaign,
           COALESCE(mh.inbound, false)::boolean AS matched_inbound,
           (COALESCE(mh.outbound, false) OR campaign_hit.hit IS NOT NULL)::boolean AS matched_outbound,
           (cm.id IS NOT NULL OR t.id IN (SELECT thread_id FROM sender_matches))::boolean AS matched_address
    FROM inbox_threads t
    LEFT JOIN message_hits mh ON mh.thread_id = t.id
    LEFT JOIN contact_matches cm ON cm.id = t.contact_id
    -- A thread's campaign leg matches when one of ITS sends (same campaign and
    -- contact, actually sent) carried matching content. The IN guard is
    -- evaluated first, against the small step_hits set, so a thread whose
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
      AND (mh.thread_id IS NOT NULL OR campaign_hit.hit IS NOT NULL
           OR cm.id IS NOT NULL OR t.id IN (SELECT thread_id FROM sender_matches))
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
       p.matched_inbound, p.matched_outbound, p.matched_address,
       COALESCE(c.email, '') AS contact_email,
       COALESCE(c.first_name, '') AS contact_first_name,
       COALESCE(c.last_name, '') AS contact_last_name,
       rl.label AS reply_label_label,
       rl.color AS reply_label_color,
       -- The snippet shown is the newest text match (best) when there is one,
       -- else the thread's newest message (latest). Every column of best is NOT
       -- NULL when best exists, so the per-column COALESCEs never mix the two
       -- rows. snippet_direction is '' only for a thread with no message at all
       -- on either leg; the text columns are then '' too.
       COALESCE(COALESCE(best.direction, latest.direction), '')::text AS snippet_direction,
       COALESCE(best.occurred_at, latest.occurred_at)::timestamptz AS snippet_occurred_at,
       -- ts_headline's regconfig must stay the one inbox_search_document and
       -- inbox_search_query use, or it would stem differently from the match
       -- and fail to highlight the very words that matched.
       --
       -- The highlight markers are chosen by the caller (private-use code
       -- points it can split on) and stripped from the INPUT first, so a
       -- message that happens to contain one can never forge a highlight.
       -- HighlightAll on the subject keeps it whole; the body is cut to
       -- fragments around the matches. Both inputs are capped for ts_headline,
       -- which re-parses its whole input: the subject at the same 1,000
       -- characters the document indexes, the body at a fifth of the indexed
       -- length — a match deeper than that still finds the thread, it just
       -- yields a lead-of-body snippet without a highlight.
       COALESCE(ts_headline('pg_catalog.english'::regconfig,
           replace(replace(left(COALESCE(best.subject, latest.subject), 1000), @highlight_start::text, ''), @highlight_stop::text, ''),
           inbox_search_query(@query::text),
           'HighlightAll=true, StartSel=' || @highlight_start::text || ', StopSel=' || @highlight_stop::text
       ), '')::text AS snippet_subject,
       COALESCE(ts_headline('pg_catalog.english'::regconfig,
           replace(replace(left(COALESCE(best.body, latest.body), 20000), @highlight_start::text, ''), @highlight_stop::text, ''),
           inbox_search_query(@query::text),
           'MaxFragments=2, MaxWords=24, MinWords=8, FragmentDelimiter=" … ", StartSel=' || @highlight_start::text || ', StopSel=' || @highlight_stop::text
       ), '')::text AS snippet_body
FROM page p
LEFT JOIN contacts c ON c.id = p.contact_id AND c.workspace_id = p.workspace_id
LEFT JOIN reply_labels rl ON rl.workspace_id = p.workspace_id AND rl.key = p.last_reply_class
-- The snippet is the NEWEST matching message on the thread, across both legs,
-- chosen only from rows the candidate sets already matched — so it needs no
-- tsvector recomputed and cannot disagree with why the thread is on the page.
-- Each branch takes its own newest row before the two are compared, so neither
-- sorts more than one thread's matches. LEFT (not CROSS) join: the page already
-- decided membership, and a missing snippet must never drop a row from it.
LEFT JOIN LATERAL (
    SELECT candidates.direction, candidates.subject, candidates.body, candidates.occurred_at
    FROM (
        (SELECT m.direction, m.subject, m.body_text AS body, m.occurred_at
         FROM message_matches mm
         JOIN inbox_messages m ON m.id = mm.id AND m.workspace_id = p.workspace_id
         WHERE p.matched_stored AND mm.thread_id = p.id
         ORDER BY mm.occurred_at DESC
         LIMIT 1)
        UNION ALL
        (SELECT 'outbound' AS direction,
                COALESCE(v.subject, st.subject) AS subject,
                COALESCE(v.body_text, st.body_text) AS body,
                s.sent_at AS occurred_at
         FROM sends s
         JOIN step_hits h ON h.campaign_id = s.campaign_id
                         AND h.step_order = s.step_order
                         AND h.variant_id IS NOT DISTINCT FROM s.variant_id
         JOIN sequence_steps st ON st.campaign_id = s.campaign_id AND st.step_order = s.step_order AND st.workspace_id = s.workspace_id
         LEFT JOIN sequence_step_variants v ON v.id = s.variant_id AND v.workspace_id = s.workspace_id
         WHERE p.matched_campaign
           AND s.workspace_id = p.workspace_id AND s.campaign_id = p.campaign_id AND s.contact_id = p.contact_id
           AND s.sent_at IS NOT NULL
         ORDER BY s.sent_at DESC
         LIMIT 1)
    ) candidates
    ORDER BY candidates.occurred_at DESC
    LIMIT 1
) best ON true
-- A thread found ONLY by address has no matching message to show, so its
-- snippet is simply its newest message on either leg (unhighlighted). Guarded
-- on the thread having no text match, so a text-matched row never pays for it;
-- each branch is one index-ordered LIMIT 1 over one thread.
LEFT JOIN LATERAL (
    SELECT newest.direction, newest.subject, newest.body, newest.occurred_at
    FROM (
        (SELECT m.direction, m.subject, m.body_text AS body, m.occurred_at
         FROM inbox_messages m
         WHERE NOT p.matched_stored AND NOT p.matched_campaign
           AND m.thread_id = p.id AND m.workspace_id = p.workspace_id
         ORDER BY m.occurred_at DESC
         LIMIT 1)
        UNION ALL
        (SELECT 'outbound' AS direction,
                COALESCE(v.subject, st.subject) AS subject,
                COALESCE(v.body_text, st.body_text) AS body,
                s.sent_at AS occurred_at
         FROM sends s
         JOIN sequence_steps st ON st.campaign_id = s.campaign_id AND st.step_order = s.step_order AND st.workspace_id = s.workspace_id
         LEFT JOIN sequence_step_variants v ON v.id = s.variant_id AND v.workspace_id = s.workspace_id
         WHERE NOT p.matched_stored AND NOT p.matched_campaign
           AND s.workspace_id = p.workspace_id AND s.campaign_id = p.campaign_id AND s.contact_id = p.contact_id
           AND s.sent_at IS NOT NULL
         ORDER BY s.sent_at DESC
         LIMIT 1)
    ) newest
    ORDER BY newest.occurred_at DESC
    LIMIT 1
) latest ON true
ORDER BY p.last_message_at DESC, p.id DESC;
