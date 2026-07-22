-- name: InsertTrackingEvent :exec
INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind, url, user_agent)
VALUES ($1,$2,$3,$4,$5,$6);
-- name: CountEngagedSendsByKind :many
-- Numerators: distinct sends with >=1 event, per kind, for a campaign.
-- Workspace-scoped for defense in depth (see CountSendsByStatus).
SELECT kind, count(DISTINCT send_id)::bigint AS n
FROM tracking_events
WHERE campaign_id = $1 AND workspace_id = $2
GROUP BY kind;
-- name: CountHumanOpens :one
-- Indicative opens: distinct sends with an 'open' event that isn't from a
-- known prefetch UA (Gmail's image proxy fetches the pixel on receipt,
-- before a human ever opens the message) and doesn't fire within 2s of the
-- send (same prefetch behavior, UA-agnostic fallback). Joined to sends for
-- sent_at.
SELECT count(DISTINCT te.send_id)::bigint
FROM tracking_events te
JOIN sends s ON s.id = te.send_id
WHERE te.campaign_id = $1 AND te.workspace_id = $2 AND te.kind = 'open'
  AND te.user_agent NOT ILIKE '%GoogleImageProxy%'
  -- sent_at IS NULL shouldn't happen for a send with a tracked open (the
  -- pixel can't fire before the send exists), but counts it as human rather
  -- than excluding it if it ever does — defensive, not a normal-flow case.
  AND (s.sent_at IS NULL OR te.created_at > s.sent_at + interval '2 seconds');
