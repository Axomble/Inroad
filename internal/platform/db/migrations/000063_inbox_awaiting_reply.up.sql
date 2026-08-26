-- "Awaiting reply": the contact spoke last on this thread, so it is waiting on
-- us. Defined once, as a function, because two queries need the identical rule
-- (the rail's awaiting_reply counter and the list's awaiting_reply scope) and a
-- count that disagreed with the list it links to would be worse than no count.
--
-- The rule has to span BOTH legs of a thread, which is the subtlety:
-- inbox_messages holds every inbound reply but only those outbound messages an
-- operator sent by hand (inbox.Store.RecordOutboundReply). A campaign's own
-- follow-up steps live in `sends` and are synthesized into the thread at READ
-- time (see ListSentOutboundStepsForThread) rather than duplicated here. So a
-- rule that looked only at inbox_messages would keep flagging a thread the
-- sequence had already answered — telling the operator to reply to something
-- that had in fact been replied to.
--
-- A thread with no inbound message at all is NOT awaiting a reply: max()
-- returns NULL, and NULL > anything is NULL, which the caller's WHERE/FILTER
-- treats as false. That is the correct answer (the contact has not spoken) and
-- it falls out of the comparison rather than needing its own branch.
--
-- STABLE + PARALLEL SAFE so the planner may cache it within a statement and
-- use it under a parallel scan. Not IMMUTABLE: it reads tables.
-- SECURITY INVOKER (the default, stated explicitly): this function must run
-- with the caller's own privileges, and every subquery is pinned on the
-- workspace_id passed in — it never widens the caller's visibility.
CREATE FUNCTION inbox_thread_awaiting_reply(
    p_thread_id    UUID,
    p_workspace_id UUID,
    p_campaign_id  UUID,
    p_contact_id   UUID
) RETURNS BOOLEAN
LANGUAGE sql
STABLE
PARALLEL SAFE
SECURITY INVOKER
AS $$
    SELECT newest_inbound.at > GREATEST(
        COALESCE(newest_manual_outbound.at, '-infinity'::timestamptz),
        COALESCE(newest_campaign_send.at,   '-infinity'::timestamptz)
    )
    FROM (
        SELECT max(m.occurred_at) AS at FROM inbox_messages m
        WHERE m.thread_id = p_thread_id AND m.workspace_id = p_workspace_id
          AND m.direction = 'inbound'
    ) AS newest_inbound,
    LATERAL (
        SELECT max(m.occurred_at) AS at FROM inbox_messages m
        WHERE m.thread_id = p_thread_id AND m.workspace_id = p_workspace_id
          AND m.direction = 'outbound'
    ) AS newest_manual_outbound,
    LATERAL (
        -- The synthesized leg. A thread with no campaign/contact link (a legacy
        -- direct-send match) has nothing to join on and contributes NULL, which
        -- COALESCE turns into "no send has happened" — correct, since there is
        -- no sequence to have answered on our behalf.
        SELECT max(s.sent_at) AS at FROM sends s
        WHERE s.workspace_id = p_workspace_id
          AND s.campaign_id = p_campaign_id
          AND s.contact_id  = p_contact_id
          AND s.sent_at IS NOT NULL
    ) AS newest_campaign_send;
$$;

-- The list's awaiting_reply scope and the rail's counter both evaluate the
-- function per candidate thread. Each of its three subqueries is a max() over
-- an existing index (idx_inbox_messages_thread on (thread_id, occurred_at),
-- and sends' own workspace/campaign/contact index), so each is an index scan
-- rather than a table scan. This partial index makes the inbound leg — the one
-- subquery evaluated for every thread — cheaper still.
CREATE INDEX idx_inbox_messages_thread_inbound
    ON inbox_messages (thread_id, occurred_at DESC)
    WHERE direction = 'inbound';
