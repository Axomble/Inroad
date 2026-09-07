-- Outbound webhooks (feature P0.3).
--
-- WHY THIS EXISTS: a self-hosted deployment needs to drive its own automation
-- off Inroad events (a reply landed, a contact opted out, an address hard
-- bounced) without polling the REST API. This is the push side: a workspace
-- registers an HTTPS endpoint, and the control plane fans a small, versioned
-- event catalog out to it, signed so the receiver can verify authenticity.
--
-- Two tables: the registered endpoints, and the per-attempt delivery log the
-- worker writes as it POSTs (and retries) each event.

-- webhook_endpoints — one registered receiver per row.
CREATE TABLE webhook_endpoints (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Tenant-scoped and NOT NULL: every read/write of an endpoint is pinned to
    -- the workspace from the authenticated JWT (docs/security.md invariant 4),
    -- and ON DELETE CASCADE makes endpoint teardown part of workspace deletion.
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The receiver URL. Validated http/https + SSRF-checked at create AND update,
    -- and re-checked in the worker just before dialing (DNS-rebinding window).
    url               TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    -- The HMAC-SHA256 signing secret, sealed under the per-workspace DEK
    -- (crypto.Keyring). The raw secret is returned to the caller exactly once at
    -- create / rotate-secret time and never again; it never appears in a read
    -- DTO, a log line, or plaintext in this column (invariant 2).
    secret_ciphertext BYTEA NOT NULL,
    -- The event types this endpoint subscribes to. An EMPTY array means "every
    -- event" — the common case, and the reason the default is '{}' rather than a
    -- NOT NULL with no default.
    event_types       TEXT[] NOT NULL DEFAULT '{}',
    -- A soft on/off switch so an operator can silence a noisy or broken endpoint
    -- without losing its configuration and secret.
    active            BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The one read this table has outside a point lookup: list every endpoint in a
-- workspace (GET /webhook-endpoints) and the dispatch fan-out's "active
-- endpoints in this workspace" scan. Both filter on workspace_id alone.
CREATE INDEX idx_webhook_endpoints_workspace ON webhook_endpoints (workspace_id);

-- webhook_deliveries — one row per (event, endpoint), carrying the exact body
-- that is / was POSTed and the attempt bookkeeping the worker updates.
CREATE TABLE webhook_deliveries (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint_id     UUID NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    -- Denormalized from the endpoint so the tenant-scoped delivery-log read and
    -- the retention purge do not have to join through webhook_endpoints (which
    -- may have been deleted). Every tenant-facing query still pins on it.
    workspace_id    UUID NOT NULL,
    event_type      TEXT NOT NULL,
    -- The exact JSON body sent to the receiver, stored so an operator can see
    -- precisely what was delivered. It carries only ids + minimal display fields
    -- (the same discipline as realtime events), never a credential.
    payload         JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'delivered', 'failed')),
    attempts        INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error      TEXT NOT NULL DEFAULT '',
    -- The HTTP status the receiver returned on the last attempt; NULL until a
    -- response (or a transport-level failure with no response) is recorded.
    response_status INT,
    -- When the next retry is due while status='pending'; NULL once the row is
    -- terminal. Feeds the reconcile sweep's partial index below.
    next_attempt_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ
);

-- The delivery-log read: one endpoint's attempts, newest first, keyset-paginated
-- (GET /webhook-endpoints/{id}/deliveries). Column order matches: equality on
-- endpoint_id, range/sort on created_at.
CREATE INDEX idx_webhook_deliveries_endpoint_created
    ON webhook_deliveries (endpoint_id, created_at DESC);

-- The reconcile sweep's scan: pending rows whose next_attempt_at has come due
-- but whose asynq task was lost. Partial so it only indexes the rows a sweep
-- cares about — terminal rows (the vast majority over time) are excluded.
CREATE INDEX idx_webhook_deliveries_due
    ON webhook_deliveries (next_attempt_at)
    WHERE status = 'pending';

-- The retention purge's scan: delete this workspace's deliveries older than the
-- retention window. Age-ordered within a workspace.
CREATE INDEX idx_webhook_deliveries_workspace_created
    ON webhook_deliveries (workspace_id, created_at);
