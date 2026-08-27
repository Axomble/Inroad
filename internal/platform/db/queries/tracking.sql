-- name: InsertTrackingEvent :exec
-- Machine events are INSERTED like any other, carrying their verdict. Dropping
-- them would make "N opens, M of them machine" unanswerable and would present a
-- truncated count as the whole truth.
INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind, url, user_agent, is_machine, machine_reason, client_ip)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,sqlc.narg(client_ip)::inet);

-- name: GetSendTrackingContext :one
-- The classifier's ordering inputs for ONE send, read on the public tracking hot
-- path, so it is a single index-only probe of idx_tracking_send_recent rather
-- than one query per fact.
--
-- Deliberately NOT workspace-scoped, and that is safe: a tracking hit carries no
-- authenticated principal to scope BY, the send id comes from an HMAC-signed
-- token, and the result never leaves the process -- it feeds a boolean verdict
-- about this same send, and no row content is returned. Scoping it would require
-- trusting a workspace id from an unauthenticated request, which is worse.
--
-- Every aggregate is explicitly cast: FILTER/COALESCE aggregates otherwise
-- generate interface{} in the sqlc model and still compile.
SELECT COALESCE(count(*) FILTER (WHERE kind = 'open' AND NOT is_machine), 0)::bigint AS human_opens,
       COALESCE(max(created_at) FILTER (WHERE kind = 'open' AND NOT is_machine), 'epoch'::timestamptz)::timestamptz AS last_human_open_at
FROM tracking_events
WHERE send_id = $1;

-- name: CountRecentSendOpensFromSubnet :one
-- The burst input: how many opens of THIS send already arrived from the same
-- address block inside the classifier's window. Same tenancy reasoning as
-- GetSendTrackingContext -- it returns a count about one send, never row data.
--
-- The block is matched with inet's subnet-containment operator, so a /24 (IPv4)
-- or /48 (IPv6) is one comparison Postgres understands as an address range,
-- rather than a string prefix match that would mis-group 203.0.113.1 with
-- 203.0.1131.
--
-- The subnet is bound as TEXT and cast in SQL because sqlc maps `inet` to
-- netip.Addr, which cannot represent a prefix at all -- an ::inet parameter
-- would silently narrow "203.0.113.0/24" to a single host and the rule would
-- never fire. netip.Prefix.String() produces exactly the CIDR literal cidr()
-- parses.
SELECT count(*)::bigint
FROM tracking_events
WHERE send_id = $1
  AND kind = 'open'
  AND created_at > $2
  AND client_ip IS NOT NULL
  AND client_ip << sqlc.arg(subnet)::text::cidr;

-- name: CountEngagedSendsByKind :many
-- Numerators: distinct sends with >=1 HUMAN event, per kind, for a campaign.
-- Workspace-scoped for defense in depth (see CountSendsByStatus).
--
-- Machine events are excluded here rather than at write time: the rows exist and
-- CountTrackingEventsByKindAndVerdict reports them, but a prefetch must never
-- reach the headline rate -- nor, once conditional branching ships, the signal a
-- branch reads.
SELECT kind, count(DISTINCT send_id)::bigint AS n
FROM tracking_events
WHERE campaign_id = $1 AND workspace_id = $2 AND NOT is_machine
GROUP BY kind;

-- name: CountTrackingEventsByKindAndVerdict :many
-- The transparency counterpart: distinct sends per (kind, verdict), so a report
-- can say "N opens, M of them machine" instead of quietly showing the filtered
-- number as if nothing had been excluded.
SELECT kind, is_machine, count(DISTINCT send_id)::bigint AS n
FROM tracking_events
WHERE campaign_id = $1 AND workspace_id = $2
GROUP BY kind, is_machine;

-- name: CountHumanOpens :one
-- Indicative opens: distinct sends with a human-classified 'open'.
--
-- This used to re-derive the judgement at READ time -- a `NOT ILIKE
-- '%GoogleImageProxy%'` literal plus a 2-second window, copy-pasted into four
-- separate queries. That put one rule in four places, so adding a scanner vendor
-- meant editing four files and any miss skewed one report against the others.
-- The verdict is now computed once, at write time, by platform/botfilter, and
-- every reader agrees by construction. The join to sends is gone with it.
SELECT count(DISTINCT send_id)::bigint
FROM tracking_events
WHERE campaign_id = $1 AND workspace_id = $2 AND kind = 'open' AND NOT is_machine;
