-- Email-authentication status per SENDING DOMAIN. Everything the sending engine
-- does — cadence, windows, pools, health gating — assumes the domain is
-- authenticated; if SPF or DMARC is missing, the bulk-sender rules spam-folder
-- or reject the mail regardless of how naturally it is paced.
--
-- Domains are DERIVED from mailboxes.email rather than entered by hand: the set
-- Inroad cares about is exactly the set it sends from, and a hand-entered list
-- would drift from it. That is why there is no FK to mailboxes and no row until
-- a domain has actually been checked — the authoritative domain list is a
-- lower(split_part(email,'@',2)) projection of mailboxes, and this table only
-- caches what DNS said about it.
--
-- One row per (workspace, domain), not per mailbox: ten mailboxes on acme.com
-- share one record and one lookup. Workspace-pinned, so two tenants sending from
-- the same domain get their own rows and never read each other's.
CREATE TABLE sending_domains (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    domain        TEXT NOT NULL,
    -- 'unknown' is a first-class state, distinct from 'failing': a resolver
    -- timeout or SERVFAIL means "could not check", and rendering that as a
    -- misconfiguration sends operators editing DNS that was already correct.
    state         TEXT NOT NULL DEFAULT 'unknown'
                    CHECK (state IN ('unknown','passing','failing')),
    spf_found     BOOLEAN NOT NULL DEFAULT FALSE,
    spf_record    TEXT NOT NULL DEFAULT '',
    -- DKIM is advisory and never decides state: selectors are not discoverable
    -- from DNS, so dkim_found = FALSE means "none of the probed selectors
    -- matched", not "this domain is unsigned".
    dkim_found    BOOLEAN NOT NULL DEFAULT FALSE,
    dkim_selector TEXT NOT NULL DEFAULT '',
    dmarc_found   BOOLEAN NOT NULL DEFAULT FALSE,
    dmarc_policy  TEXT NOT NULL DEFAULT '',
    -- NULL until a check COMPLETES. A check that ended 'unknown' leaves this
    -- untouched, so the sweep retries it on the next tick instead of waiting out
    -- the staleness window on an answer it never got.
    checked_at    TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, domain)
);

-- No index beyond the UNIQUE: every read is either that key (the per-domain
-- check) or a scan the mailbox set already bounds (one row per domain a
-- workspace sends from, and the sweep's staleness scan over all of them). An
-- index on checked_at would cost writes to serve a query that reads a table this
-- small end to end anyway.
