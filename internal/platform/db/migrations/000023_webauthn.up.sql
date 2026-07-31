-- Passkeys / WebAuthn: stored public-key credentials + short-TTL, single-use
-- ceremony challenges.
--
-- A passkey is a USER-level credential (like TOTP, migration 000021): it follows
-- the human across every workspace they belong to, so these tables key on user_id
-- and carry no workspace_id. Nothing here is a secret at rest — a WebAuthn public
-- key is public by definition and the private key never leaves the authenticator —
-- so unlike the TOTP secret these columns are stored in the clear (no Sealer).

CREATE TABLE webauthn_credentials (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Raw credential id (the authenticator's handle). Globally unique: it is the
    -- lookup key for discoverable login, where the user is resolved ONLY from the
    -- authenticated credential, never a client-supplied id.
    credential_id   BYTEA NOT NULL UNIQUE,
    -- COSE-encoded public key used to verify assertions. Public, not a secret.
    public_key      BYTEA NOT NULL,
    -- Signature counter (RFC: monotonic per authenticator). Verified to never
    -- regress on login (clone detection) and advanced on every successful login.
    -- BIGINT holds the library's uint32 without overflow.
    sign_count      BIGINT NOT NULL DEFAULT 0,
    aaguid          BYTEA,
    -- Comma-joined AuthenticatorTransport hints (e.g. "internal,hybrid"); advisory.
    transports      TEXT NOT NULL DEFAULT '',
    attestation_type TEXT NOT NULL DEFAULT '',
    -- Backup-eligible / backup-state flags. Persisted because the library rejects a
    -- login whose backup-eligible flag disagrees with the value seen at registration.
    backup_eligible BOOLEAN NOT NULL DEFAULT false,
    backup_state    BOOLEAN NOT NULL DEFAULT false,
    -- User-facing label for the manage UI ("MacBook Touch ID"). Never security-bearing.
    label           TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at    TIMESTAMPTZ
);
CREATE INDEX idx_webauthn_credentials_user ON webauthn_credentials (user_id);

CREATE TABLE webauthn_challenges (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- SHA-256 of the opaque session token handed to the client between begin and
    -- finish; the raw token lives only with the client. The lookup key for the
    -- ceremony's server-stored state.
    session_key  BYTEA NOT NULL UNIQUE,
    -- NULL for a discoverable (usernameless) login, where no user is known until the
    -- authenticated credential resolves one. Set to the authed user for registration.
    user_id      UUID REFERENCES users(id) ON DELETE CASCADE,
    -- Serialized library SessionData: it embeds the server-generated challenge plus
    -- the ceremony parameters (UV requirement, allowed credentials). Stored
    -- server-side and never trusted from the client so a client-echoed challenge
    -- can never be substituted.
    session_data JSONB NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('register', 'login')),
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_webauthn_challenges_session_key ON webauthn_challenges (session_key);
