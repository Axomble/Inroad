-- OAuth 2.1 token-endpoint storage (P6b): the tables the /oauth2/token exchange
-- writes and the introspection/revocation/verifier paths read. This is Inroad acting
-- as an OAuth PROVIDER; it completes the P6a authorize side (migration 000026).
--
-- Access tokens are OPAQUE and stored HASHED (not JWTs) so they are introspectable
-- and individually revocable, matching the revocable-session philosophy: the verifier
-- re-checks revoked_at + expiry on every request, so a revoke takes effect immediately.
-- The raw token (a recognizable `inoa_` prefix + high-entropy secret) is returned to
-- the client exactly once; only its SHA-256 is ever persisted.

-- Short-TTL (~1h) bearer access tokens.
CREATE TABLE oauth_access_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- SHA-256 of the raw opaque token. UNIQUE so the verifier's by-hash lookup and the
    -- client-scoped revoke resolve exactly one row. Only the digest is stored.
    token_hash   BYTEA NOT NULL UNIQUE,
    -- The client the token was issued to (its public client_id). A client may revoke
    -- only its own tokens (RFC 7009), enforced by matching this column.
    client_id    TEXT NOT NULL,
    -- The rotation family this access token was minted in (shared with its sibling
    -- refresh token and every rotation successor). When reuse detection kills a refresh
    -- family, the matching access tokens are revoked by the SAME family_id, so a
    -- compromised chain's access tokens die on their next request rather than living out
    -- their ~1h TTL. Mirrors oauth_refresh_tokens.family_id.
    family_id    UUID NOT NULL,
    -- The resource owner + workspace the grant is pinned to. Every scoped request the
    -- token authenticates is bound to THIS workspace, never a caller-supplied one.
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The granted scope subset (a subset of the code's scopes). RequireScope attenuates
    -- the principal to exactly these.
    scopes       TEXT[] NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    -- Set on explicit revoke (RFC 7009) or a family revoke on reuse detection. The
    -- verifier rejects a revoked token at once.
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Family revoke (reuse detection kills the whole chain's access tokens too) scans by
-- family_id — mirrors the refresh-token family index below.
CREATE INDEX idx_oauth_access_tokens_family ON oauth_access_tokens (family_id);

-- Longer-TTL (~30d) rotating refresh tokens with reuse detection. Every refresh token
-- belongs to a rotation FAMILY (family_id): a token exchange rotates the presented
-- token (stamps consumed_at) and issues a successor in the SAME family. Presenting an
-- already-consumed token (reuse) revokes the ENTIRE family — the exact strategy the P1
-- session refresh uses, reused here as the design calls for.
CREATE TABLE oauth_refresh_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- SHA-256 of the raw opaque refresh token (`inor_` prefix). UNIQUE for the by-hash
    -- rotation lookup. Only the digest is stored; the raw value is returned once.
    token_hash   BYTEA NOT NULL UNIQUE,
    -- The rotation family. All successors of an initially-issued refresh token share it,
    -- so a single UPDATE ... WHERE family_id revokes the whole chain on reuse.
    family_id    UUID NOT NULL,
    client_id    TEXT NOT NULL,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The scope set carried forward on rotation. A refresh may only NARROW this subset,
    -- never widen it.
    scopes       TEXT[] NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    -- Set when the token is rotated (its single-use redemption). Presenting a token that
    -- already has consumed_at set is REUSE and revokes the family.
    consumed_at  TIMESTAMPTZ,
    -- Set on explicit or family revoke; a revoked token can never rotate.
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Family revoke (reuse detection + RFC 7009 refresh revoke) scans by family_id.
CREATE INDEX idx_oauth_refresh_tokens_family ON oauth_refresh_tokens (family_id);
