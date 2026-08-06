-- name: GetRecipientDomainESP :one
-- The SEND PATH's read: one recipient domain's cached ESP, workspace-pinned.
--
-- checked_at IS NOT NULL is part of the predicate, not a detail: a row whose
-- lookup never completed carries the 'unknown' default, and returning it would
-- be indistinguishable from a real answer of "neither". No row means "not
-- cached", which the caller treats as unknown and skips matching for — never a
-- reason to block on DNS or fail a send.
--
-- Served by the (workspace_id, domain) UNIQUE as a single point lookup, which is
-- what makes it affordable inside resolveSender.
SELECT esp FROM recipient_domains
WHERE workspace_id = $1 AND domain = $2 AND checked_at IS NOT NULL;

-- name: UpsertRecipientDomain :exec
-- Records one COMPLETED lookup. checked_at is set by the statement and never by
-- the caller: it means "when this server finished a lookup", which a
-- caller-supplied timestamp could not mean. A lookup that did NOT complete must
-- not reach here at all — writing it would stamp checked_at and hide the domain
-- from the sweep for a full staleness window (the sending_domains rule, 000036).
INSERT INTO recipient_domains (workspace_id, domain, esp, mx_host, checked_at)
VALUES ($1, $2, $3, $4, now())
ON CONFLICT (workspace_id, domain) DO UPDATE SET
    esp        = EXCLUDED.esp,
    mx_host    = EXCLUDED.mx_host,
    checked_at = now();

-- name: ListStaleRecipientDomains :many
-- The sweep's fan-out. Deliberately NOT "every contact domain": only domains of
-- contacts on an ACTIVE enrollment that has no mailbox pinned yet, which is
-- exactly the set where ESP matching can still change an outcome (the sender is
-- pinned write-once at the first send and a thread is never re-routed). That
-- bound is what keeps a table sized by the contact list from being filled by it.
--
-- Global across workspaces by design — it is infrastructure maintenance, not a
-- tenant read — and each row carries the workspace its write-back is pinned to.
--
-- LIMIT bounds one tick's work so a large import cannot turn a sweep into an
-- unbounded DNS run; whatever is left stays stale and the next tick takes it.
SELECT DISTINCT
    e.workspace_id,
    lower(split_part(ct.email, '@', 2))::text AS domain
FROM sequence_enrollments e
JOIN contacts ct ON ct.id = e.contact_id AND ct.workspace_id = e.workspace_id
LEFT JOIN recipient_domains rd
       ON rd.workspace_id = e.workspace_id
      AND rd.domain = lower(split_part(ct.email, '@', 2))
WHERE e.status = 'active'
  AND e.mailbox_id IS NULL
  AND position('@' in ct.email) > 0
  AND (rd.checked_at IS NULL OR rd.checked_at < @cutoff)
ORDER BY 1, 2
LIMIT @row_limit;

-- name: DeleteExpiredRecipientDomains :execrows
-- Retention. A domain nothing has mailed since the cutoff is dropped: only
-- actively-enrolled domains are refreshed (ListStaleRecipientDomains), so a
-- domain that stops being mailed stops being touched and ages out here. Losing a
-- row costs one DNS lookup if that domain is ever enrolled again — a miss reads
-- as 'unknown' and falls back to the full pool, so nothing breaks meanwhile.
--
-- COALESCE(checked_at, created_at) matches idx_recipient_domains_retention
-- verbatim so this seeks rather than scans, and so a row whose lookup never
-- completed ages out on its creation date instead of living forever.
DELETE FROM recipient_domains
WHERE COALESCE(checked_at, created_at) < @cutoff;
