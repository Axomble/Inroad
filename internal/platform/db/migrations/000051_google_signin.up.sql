-- Google sign-in / sign-up (Inroad as an OAuth CLIENT to Google, for LOGIN --
-- distinct from the mailbox-connect flow) plus workspace onboarding state.

-- A federated account has no password at all. Until now every user row carried
-- one (AcceptInviteTx even refuses to create a user without one), so the column
-- has to become nullable before a Google-only signup can exist. Password login
-- must treat NULL as "this account has no password" and reject -- never as an
-- empty-string comparison (see identity.Service.Authenticate).
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

-- One row per (provider, provider_subject) => the external identity that owns a
-- local user. Keyed on the provider's immutable subject id, NEVER on email: a
-- user who changes their Google address keeps the same `sub`, so matching on
-- email would silently mint a second account for the same person. Email is used
-- only to find an existing account to LINK the first time.
CREATE TABLE user_identities (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,
    provider_subject TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_subject)
);
-- "which identities does this user have" (account-settings surface, and the
-- cascade target when a user is deleted).
CREATE INDEX idx_user_identities_user ON user_identities (user_id);

-- NULL = onboarding not finished. A workspace created by a Google signup gets a
-- name derived server-side from the Google claims, which the onboarding modal
-- then replaces; completing onboarding sets the name and this timestamp in one
-- transaction.
ALTER TABLE workspaces ADD COLUMN onboarding_completed_at TIMESTAMPTZ;

-- Server-side, single-use state for a LOGIN OAuth flow. The signed `state`
-- parameter alone (internal/platform/oauthstate) proves the server minted it and
-- bounds replay by TTL, but nothing stopped a leaked state URL being replayed
-- inside that window -- and for a login flow a replay is account access, not a
-- stray mailbox binding (docs security invariant 10 listed this store as
-- deferred hardening). Consuming a row here makes each state usable exactly once.
--
-- Only the SHA-256 of the nonce is stored: the raw nonce travels in a URL, so it
-- is treated as a bearer credential like sessions.token_hash and
-- user_tokens.token_hash.
--
-- code_verifier is the PKCE verifier in plaintext, because the token exchange has
-- to send it to Google verbatim. It is not a user credential: it is a random,
-- 10-minute, single-use value that is worthless without both our client secret
-- and the matching authorization code.
--
-- invite_token_hash is the HASH of a pending workspace invite's token, so an
-- invite bearer credential is never stored (or carried through Google) in raw
-- form -- workspace_invites is looked up by hash anyway.
--
-- return_to is the in-app path to send the browser to once a session exists. It
-- lives HERE rather than in the state parameter because it is the one value in
-- this flow a caller supplies: keeping it server-side means the redirect target
-- cannot be swapped by editing a URL, and it is validated as a same-origin path
-- before it is ever stored (identity.safeReturnTo) so this can never become an
-- open redirect.
CREATE TABLE oauth_login_states (
    nonce_hash        BYTEA PRIMARY KEY,
    purpose           TEXT NOT NULL,
    code_verifier     TEXT NOT NULL,
    invite_token_hash BYTEA,
    return_to         TEXT,
    expires_at        TIMESTAMPTZ NOT NULL,
    consumed_at       TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Serves the maintenance sweep's expires_at range scan (PurgeExpiredSecurityArtifacts).
CREATE INDEX idx_oauth_login_states_expires ON oauth_login_states (expires_at);

-- The Google callback resolves a brand-new email to a pending invite ACROSS
-- workspaces, so it looks invites up by email alone. The existing
-- idx_invites_pending_ws_email leads with workspace_id and cannot serve that.
CREATE INDEX idx_invites_pending_email ON workspace_invites (email) WHERE status = 'pending';
