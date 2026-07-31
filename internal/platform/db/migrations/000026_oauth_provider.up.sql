-- OAuth 2.1 authorization-server tables (Inroad acting as an OAuth PROVIDER).
--
-- This is the DECOUPLED authorize side (P6a): dynamic client registration, the
-- authorization endpoint, and the consent handoff. It is a self-contained domain
-- (internal/app/oauthprovider) that no product domain imports. The token endpoint
-- and refresh/access-token tables are P6b; the oauth_authorization_codes table
-- created here is exactly what that later token exchange will consume.
--
-- NOTE: this is Inroad-as-OAuth-provider, unrelated to the mailbox-connect flow
-- (Inroad as an OAuth CLIENT to Google/Microsoft, migration 000012).

-- Registered third-party clients (RFC 7591 dynamic client registration).
CREATE TABLE oauth_clients (
    id                         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Public, non-secret client identifier the /authorize + token endpoints resolve
    -- a request by. UNIQUE so a presented client_id maps to exactly one row.
    client_id                  TEXT NOT NULL UNIQUE,
    -- SHA-256 of the client secret. NULL for a PUBLIC (PKCE) client, which has no
    -- secret at all. Only the digest is stored; the raw secret is shown once at
    -- registration and never re-derivable. Never returned in a response.
    client_secret_hash         BYTEA,
    -- Operator-facing label shown on the consent screen ("Acme Analytics").
    client_name                TEXT NOT NULL,
    -- Exact-match redirect-URI allowlist. /authorize matches the request's
    -- redirect_uri against this by EXACT string equality (no normalization), so a
    -- validated URI is always one the operator registered — anti-open-redirect.
    redirect_uris              TEXT[] NOT NULL,
    grant_types                TEXT[] NOT NULL DEFAULT '{}',
    response_types             TEXT[] NOT NULL DEFAULT '{}',
    -- The client's registered/allowed scopes: a subset of the OAuth-grantable
    -- allowlist (auth.OAuthGrantableScopes). A request may only narrow within this.
    scopes                     TEXT[] NOT NULL DEFAULT '{}',
    -- 'public' (PKCE, no secret) or 'confidential' (has a secret).
    client_type                TEXT NOT NULL,
    -- RFC 7591 token_endpoint_auth_method: 'none' for public, otherwise a
    -- client_secret_* method for confidential.
    token_endpoint_auth_method TEXT NOT NULL,
    -- The session user + workspace that registered the client, when registration
    -- carried a session. Both NULL for an anonymous (unauthenticated) registration:
    -- such a client is constrained to non-privileged scopes + validated redirect
    -- URIs, but is not listable/revocable through the workspace admin API. SET NULL
    -- on user delete (the client outlives its creator); CASCADE on workspace delete.
    created_by_user_id         UUID REFERENCES users(id) ON DELETE SET NULL,
    workspace_id               UUID REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Set when revoked; a revoked client can no longer start an authorization.
    revoked_at                 TIMESTAMPTZ
);
-- Admin management lists a workspace's clients; the index keeps that scan cheap.
CREATE INDEX idx_oauth_clients_workspace ON oauth_clients (workspace_id);

-- Short-TTL, single-use authorization request. /authorize persists the FULLY
-- VALIDATED request here (keyed by an opaque consent_id) and hands the user off to
-- the SPA consent screen; the consent decision consumes this row. It never holds an
-- unvalidated parameter — client + redirect_uri + PKCE + scope are all checked
-- before a row is written.
CREATE TABLE oauth_authorization_requests (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Opaque high-entropy id carried in the SPA consent URL. UNIQUE: a consent
    -- decision resolves exactly one pending request.
    consent_id            TEXT NOT NULL UNIQUE,
    client_id             TEXT NOT NULL,
    redirect_uri          TEXT NOT NULL,
    scopes                TEXT[] NOT NULL,
    -- The client's opaque `state`, echoed back on the eventual redirect. NULL when
    -- the client sent none (nothing is echoed in that case).
    state                 TEXT,
    code_challenge        TEXT NOT NULL,
    code_challenge_method TEXT NOT NULL,
    -- The resource owner who is granting consent, resolved from the P1 session at
    -- /authorize — never from a request parameter.
    user_id               UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    expires_at            TIMESTAMPTZ NOT NULL,
    -- Single-use: set when the consent decision (approve/deny) consumes the request.
    consumed_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Remembered per-(user, client) consent. A later /authorize whose requested scopes
-- are all covered here SKIPS the consent screen and issues a code immediately.
CREATE TABLE oauth_consents (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_id    TEXT NOT NULL,
    scopes       TEXT[] NOT NULL,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One consent row per (user, client); a grant upserts it.
    UNIQUE (user_id, client_id)
);

-- Single-use authorization codes (consumed by the P6b token endpoint). The raw code
-- goes to the client via the redirect; only its SHA-256 is stored. The row binds the
-- code to the client, redirect_uri, PKCE challenge, scopes, user, and workspace so
-- the token exchange can verify EVERY one of them.
CREATE TABLE oauth_authorization_codes (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- SHA-256 of the high-entropy raw code. UNIQUE so the token endpoint's
    -- single-use consume (UPDATE ... WHERE code_hash = $1 AND consumed_at IS NULL)
    -- resolves exactly one row.
    code_hash             BYTEA NOT NULL UNIQUE,
    client_id             TEXT NOT NULL,
    redirect_uri          TEXT NOT NULL,
    code_challenge        TEXT NOT NULL,
    code_challenge_method TEXT NOT NULL,
    scopes                TEXT[] NOT NULL,
    user_id               UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- SHORT TTL (~60s): a code is exchanged immediately after issue.
    expires_at            TIMESTAMPTZ NOT NULL,
    -- Single-use: set by the token endpoint's atomic consume (P6b).
    consumed_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);
