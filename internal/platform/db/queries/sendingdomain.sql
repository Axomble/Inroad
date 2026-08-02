-- Sending-domain email authentication (SPF/DKIM/DMARC).
--
-- The domain list is DERIVED, not stored: every read projects
-- lower(split_part(email,'@',2)) over the workspace's mailboxes and LEFT JOINs
-- the cached DNS answer, so a domain whose mailbox was just connected shows up
-- immediately as 'unknown' with a null checked_at rather than being missing
-- until some writer remembers to insert it.

-- name: ListSendingDomains :many
SELECT
    lower(split_part(m.email, '@', 2))::text AS domain,
    COUNT(*)::bigint AS mailbox_count,
    COALESCE(d.state, 'unknown')::text AS state,
    COALESCE(d.spf_found, FALSE)::boolean AS spf_found,
    COALESCE(d.spf_record, '')::text AS spf_record,
    COALESCE(d.dkim_found, FALSE)::boolean AS dkim_found,
    COALESCE(d.dkim_selector, '')::text AS dkim_selector,
    COALESCE(d.dmarc_found, FALSE)::boolean AS dmarc_found,
    COALESCE(d.dmarc_policy, '')::text AS dmarc_policy,
    d.checked_at
FROM mailboxes m
LEFT JOIN sending_domains d
       ON d.workspace_id = m.workspace_id
      AND d.domain = lower(split_part(m.email, '@', 2))
WHERE m.workspace_id = $1
  AND position('@' in m.email) > 0
GROUP BY 1, d.state, d.spf_found, d.spf_record, d.dkim_found, d.dkim_selector,
         d.dmarc_found, d.dmarc_policy, d.checked_at
ORDER BY 1;

-- Zero rows means the workspace has no mailbox on that domain. That is the 404
-- the check endpoint returns BEFORE any DNS lookup happens, so the resolver
-- cannot be pointed at an arbitrary name through this API.
-- name: GetSendingDomain :one
SELECT
    lower(split_part(m.email, '@', 2))::text AS domain,
    COUNT(*)::bigint AS mailbox_count,
    COALESCE(d.state, 'unknown')::text AS state,
    COALESCE(d.spf_found, FALSE)::boolean AS spf_found,
    COALESCE(d.spf_record, '')::text AS spf_record,
    COALESCE(d.dkim_found, FALSE)::boolean AS dkim_found,
    COALESCE(d.dkim_selector, '')::text AS dkim_selector,
    COALESCE(d.dmarc_found, FALSE)::boolean AS dmarc_found,
    COALESCE(d.dmarc_policy, '')::text AS dmarc_policy,
    d.checked_at
FROM mailboxes m
LEFT JOIN sending_domains d
       ON d.workspace_id = m.workspace_id
      AND d.domain = lower(split_part(m.email, '@', 2))
WHERE m.workspace_id = @workspace_id
  AND lower(split_part(m.email, '@', 2)) = @domain
GROUP BY 1, d.state, d.spf_found, d.spf_record, d.dkim_found, d.dkim_selector,
         d.dmarc_found, d.dmarc_policy, d.checked_at;

-- checked_at is set by the statement, never by the caller: it means "when this
-- server completed a check", and a caller-supplied timestamp could not mean that.
-- name: UpsertSendingDomain :one
INSERT INTO sending_domains (
    workspace_id, domain, state,
    spf_found, spf_record, dkim_found, dkim_selector, dmarc_found, dmarc_policy,
    checked_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())
ON CONFLICT (workspace_id, domain) DO UPDATE SET
    state         = EXCLUDED.state,
    spf_found     = EXCLUDED.spf_found,
    spf_record    = EXCLUDED.spf_record,
    dkim_found    = EXCLUDED.dkim_found,
    dkim_selector = EXCLUDED.dkim_selector,
    dmarc_found   = EXCLUDED.dmarc_found,
    dmarc_policy  = EXCLUDED.dmarc_policy,
    checked_at    = now()
RETURNING checked_at;

-- The sweep's fan-out: every derived domain, across all workspaces, whose last
-- COMPLETED check is older than the cutoff (or which has never been checked).
-- Global by design — it is infrastructure maintenance, not a tenant read — and
-- each row carries its workspace so the write back is workspace-pinned.
-- name: ListStaleSendingDomains :many
SELECT DISTINCT
    m.workspace_id,
    lower(split_part(m.email, '@', 2))::text AS domain
FROM mailboxes m
LEFT JOIN sending_domains d
       ON d.workspace_id = m.workspace_id
      AND d.domain = lower(split_part(m.email, '@', 2))
WHERE position('@' in m.email) > 0
  AND (d.checked_at IS NULL OR d.checked_at < @cutoff)
ORDER BY 1, 2;
