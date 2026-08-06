-- Which email service provider handles a RECIPIENT domain's mail, cached from
-- its MX records. Read at sender selection so a campaign with a mixed pool can
-- send Google→Google and Microsoft→Microsoft, which keeps the message inside one
-- operator's infrastructure and measurably helps placement.
--
-- Shaped after sending_domains (000036) and it copies that table's two best
-- conventions deliberately:
--   * 'unknown' is a first-class state, distinct from 'other'. 'other' means
--     "checked, and it is neither Google nor Microsoft"; 'unknown' means "not
--     checked". Collapsing them would make a resolver timeout indistinguishable
--     from a real answer.
--   * checked_at stays NULL until a lookup COMPLETES, so a transient resolver
--     failure is retried on the next sweep instead of waiting out the staleness
--     window on an answer that never arrived.
--
-- It differs from sending_domains in the one way that matters operationally.
-- sending_domains is bounded by the mailboxes a deployment OWNS — tens of rows,
-- which is why it carries no index beyond its UNIQUE and needs no eviction. This
-- table is bounded by the CONTACT LIST and can reach hundreds of thousands of
-- rows, so it ships with a retention policy from day one (see the index below,
-- and worker/recipientesp for the constants):
--   * rows are only ever created for a domain on an ACTIVE enrollment that has
--     not yet been pinned to a mailbox — the only moment ESP matching can apply
--     — never for the whole contacts table;
--   * a row is re-looked-up when it goes stale (MX records change by hand and
--     rarely, so the window is long);
--   * a row nothing has mailed for the retention window is DELETED. Since only
--     actively-enrolled domains are refreshed, a domain that stops being mailed
--     stops being refreshed and ages out on its own.
--
-- A stale or evicted row is never a correctness problem: a cache miss reads as
-- 'unknown', which skips matching and falls back to the full sender pool.
CREATE TABLE recipient_domains (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    domain       TEXT NOT NULL,
    esp          TEXT NOT NULL DEFAULT 'unknown'
                   CHECK (esp IN ('unknown','google','microsoft','other')),
    -- The primary MX as observed. Diagnostic only — nothing routes on it — but
    -- it is the only thing that explains to an operator why a domain read
    -- 'other'.
    mx_host      TEXT NOT NULL DEFAULT '',
    -- NULL until a lookup COMPLETES; see the header.
    checked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One row per (workspace, domain): every contact at acme.com shares one
    -- record and one lookup. Workspace-pinned, so two tenants mailing the same
    -- domain get their own rows and never read each other's. Also the index the
    -- send path's point lookup and the sweep's staleness join both seek on.
    UNIQUE (workspace_id, domain)
);

-- Eviction's access path. Unlike sending_domains, this table is large enough
-- that a retention pass without an index would seq-scan every night; the
-- expression matches DeleteExpiredRecipientDomains exactly (COALESCE, so a row
-- whose lookup never completed ages out on created_at rather than living
-- forever). Cheap to maintain: a row is written once when its domain is first
-- resolved and again only when it goes stale.
CREATE INDEX idx_recipient_domains_retention
    ON recipient_domains (COALESCE(checked_at, created_at));
