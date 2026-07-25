-- Per-workspace data-encryption key (DEK), wrapped by the KEK (KeyProvider).
-- The plaintext DEK is never stored. ON DELETE CASCADE gives crypto-shredding:
-- deleting a workspace destroys its DEK, rendering all its sealed data
-- (mailbox creds, OAuth tokens) permanently unrecoverable.
CREATE TABLE workspace_deks (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    wrapped_dek  bytea       NOT NULL,
    key_provider text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
