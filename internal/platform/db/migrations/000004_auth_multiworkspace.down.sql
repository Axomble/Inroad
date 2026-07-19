DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS workspace_members;
DROP TYPE IF EXISTS member_role;
DROP TABLE IF EXISTS users;

-- restore v1 users shape so 000001 down still composes
CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'owner',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, email)
);
CREATE INDEX idx_users_email ON users (email);
