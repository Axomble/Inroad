-- Per-IP worker routing (spec §15). Two tables with DIFFERENT trust domains:
--
--   workers                     — GLOBAL infrastructure registry, NOT tenant data.
--                                 One row per running worker, refreshed on its
--                                 heartbeat; the assigner reads it to find live
--                                 workers. Never returned on a tenant-facing API
--                                 (security invariant §17.9).
--   mailbox_worker_assignments  — TENANT data, workspace-pinned. Pins a mailbox's
--                                 outbound traffic to ONE worker so all of that
--                                 mailbox's mail egresses from a single IP (the
--                                 deliverability win). CASCADEs from both parents
--                                 so deleting a mailbox or workspace cleans it up.
CREATE TABLE workers (
    worker_id     TEXT PRIMARY KEY,
    egress_ip     TEXT NOT NULL DEFAULT '',
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE mailbox_worker_assignments (
    mailbox_id    UUID PRIMARY KEY REFERENCES mailboxes(id) ON DELETE CASCADE,
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    worker_id     TEXT NOT NULL,
    assigned_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The least-loaded pick counts existing assignments per worker (a correlated
-- count grouped by worker_id); index worker_id so that stays index-backed as the
-- fleet and assignment table grow.
CREATE INDEX mailbox_worker_assignments_worker ON mailbox_worker_assignments(worker_id);
