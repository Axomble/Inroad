-- 000007/000008 are reserved for the concurrent sequencing branch; this
-- branch's head was 000006, so auth phase 2 continues at 000009.

CREATE TYPE user_token_kind AS ENUM ('email_verify', 'password_reset');
CREATE TABLE user_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind        user_token_kind NOT NULL,
    token_hash  BYTEA NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_tokens_hash ON user_tokens (token_hash);
CREATE INDEX idx_user_tokens_user_kind ON user_tokens (user_id, kind, created_at);

CREATE TYPE invite_status AS ENUM ('pending', 'accepted', 'revoked');
CREATE TABLE workspace_invites (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email        CITEXT NOT NULL,
    role         member_role NOT NULL DEFAULT 'member',
    token_hash   BYTEA NOT NULL,
    invited_by   UUID NOT NULL REFERENCES users(id),
    status       invite_status NOT NULL DEFAULT 'pending',
    expires_at   TIMESTAMPTZ NOT NULL,
    accepted_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_invites_hash ON workspace_invites (token_hash);
CREATE UNIQUE INDEX idx_invites_pending_ws_email
    ON workspace_invites (workspace_id, email) WHERE status = 'pending';
