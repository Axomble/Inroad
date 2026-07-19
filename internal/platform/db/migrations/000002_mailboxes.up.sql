CREATE TABLE mailboxes (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    provider              TEXT NOT NULL DEFAULT 'smtp',   -- smtp | gmail | m365 (later)
    email                 TEXT NOT NULL,
    display_name          TEXT NOT NULL DEFAULT '',

    -- SMTP (outbound)
    smtp_host             TEXT NOT NULL DEFAULT '',
    smtp_port             INTEGER NOT NULL DEFAULT 587,
    smtp_username         TEXT NOT NULL DEFAULT '',

    -- IMAP (reply/bounce polling)
    imap_host             TEXT NOT NULL DEFAULT '',
    imap_port             INTEGER NOT NULL DEFAULT 993,
    imap_username         TEXT NOT NULL DEFAULT '',

    -- Envelope-encrypted secret (SMTP/IMAP password or app password)
    secret_ciphertext     TEXT NOT NULL,
    use_tls               BOOLEAN NOT NULL DEFAULT TRUE,

    -- Sending controls (PRD 9.1.5)
    daily_cap             INTEGER NOT NULL DEFAULT 50,
    min_interval_seconds  INTEGER NOT NULL DEFAULT 120,

    -- Ramp / warmup (PRD 9.5.1)
    ramp_enabled          BOOLEAN NOT NULL DEFAULT TRUE,
    ramp_start_cap        INTEGER NOT NULL DEFAULT 5,
    ramp_days             INTEGER NOT NULL DEFAULT 30,

    -- Health / status (PRD 9.1.4)
    status                TEXT NOT NULL DEFAULT 'active',  -- active | paused | error
    last_error            TEXT NOT NULL DEFAULT '',
    last_send_at          TIMESTAMPTZ,
    last_poll_at          TIMESTAMPTZ,

    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, email)
);

CREATE INDEX idx_mailboxes_workspace ON mailboxes (workspace_id);
