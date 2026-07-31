-- TOTP 2FA + recovery codes + login-gate challenges.
--
-- 2FA is a USER-level capability, NOT workspace-scoped: a user's TOTP secret and
-- recovery codes follow the user across every workspace they belong to, so these
-- tables key on user_id and carry no workspace_id. The TOTP secret is sealed with
-- a SERVER-level key (an HKDF subkey of INROAD_MASTER_KEY, info label
-- inroad-user-secret-v1) rather than a per-workspace DEK, precisely because no
-- single workspace owns it — the per-workspace Keyring.SealerFor model does not
-- fit a cross-workspace user secret.

CREATE TABLE user_totp (
    user_id           UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    -- Sealed (crypto.Sealer v2 envelope) TOTP secret. Never returned after
    -- enrollment, never logged.
    secret_ciphertext TEXT NOT NULL,
    -- NULL until the user proves possession with a valid code; a confirmed
    -- second factor is what the login gate enforces.
    confirmed_at      TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE user_recovery_codes (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- argon2id hash of the single-use recovery code (reuses the password params).
    -- The raw code is shown to the user exactly once and is never re-derivable.
    code_hash  TEXT NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Login verify scans a user's still-unused codes; the partial index keeps that
-- lookup cheap and lets used codes drop out of it.
CREATE INDEX idx_recovery_codes_user_unused ON user_recovery_codes (user_id) WHERE used_at IS NULL;

CREATE TABLE two_factor_challenges (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- SHA-256 hash of the opaque challenge token; the raw token lives only with
    -- the client between login and 2fa/verify.
    challenge_hash BYTEA NOT NULL,
    attempts       INT NOT NULL DEFAULT 0,
    ip             INET,
    consumed_at    TIMESTAMPTZ,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_2fa_challenges_hash ON two_factor_challenges (challenge_hash);
-- Per-IP throttle on challenge issuance counts recent rows from an address.
CREATE INDEX idx_2fa_challenges_ip ON two_factor_challenges (ip, created_at);
