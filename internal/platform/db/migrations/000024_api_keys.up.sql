-- Scoped API keys: workspace-level machine credentials with an attenuated scope
-- set, an optional CIDR allowlist, an optional per-minute rate limit, and an
-- optional expiry.
--
-- Unlike TOTP/passkeys (USER-level, migrations 000021/000023), an API key is a
-- WORKSPACE-level credential: it authenticates as the workspace that owns it,
-- carrying only a SUBSET of that workspace's authority (see internal/app/auth/
-- scopes.go). It therefore keys on workspace_id and is deleted by the same
-- ON DELETE CASCADE crypto-shredding path as the workspace's other data.
--
-- The raw secret is NEVER stored: only the SHA-256 of the 256-bit random secret
-- is persisted (`secret_hash`). The public, non-secret `prefix` is the O(1)
-- lookup key the verifier resolves a presented token by, before a constant-time
-- compare of the secret hash. The token shown to the operator exactly once is
-- `inrd_<prefix>_<secret>`.

CREATE TABLE api_keys (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The session user who minted the key. SET NULL (not CASCADE) on user delete:
    -- the key belongs to the workspace, not the person, so it outlives its creator.
    created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
    -- Operator-facing label for the manage UI ("CI deploy key"). Not security-bearing.
    name               TEXT NOT NULL,
    -- Public, non-secret short id embedded in the token. UNIQUE so a presented
    -- token resolves to exactly one key in one indexed lookup.
    prefix             TEXT NOT NULL UNIQUE,
    -- SHA-256 of the random 256-bit secret. The raw secret is shown once and is
    -- never re-derivable; only this digest is stored. Never returned in a response.
    secret_hash        BYTEA NOT NULL,
    -- Granted scopes: a subset of the owned vocabulary (auth.AllScopes). The
    -- machine principal minted from this key holds exactly these.
    scopes             TEXT[] NOT NULL DEFAULT '{}',
    -- Optional CIDR allowlist (stored as canonical text; validated + parsed to
    -- netip.Prefix at the boundary). NULL/empty = no IP restriction.
    ip_allowlist       TEXT[],
    -- Optional fixed-window per-minute request cap. NULL = unlimited.
    rate_limit_per_min INT,
    -- Optional expiry; NULL = never expires.
    expires_at         TIMESTAMPTZ,
    -- Set when the key is revoked; a revoked key never authenticates again.
    revoked_at         TIMESTAMPTZ,
    -- Best-effort last-use timestamp, touched asynchronously on a successful verify.
    last_used_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Management lists a workspace's keys; the index keeps that tenant-pinned scan cheap.
CREATE INDEX idx_api_keys_workspace ON api_keys (workspace_id);
