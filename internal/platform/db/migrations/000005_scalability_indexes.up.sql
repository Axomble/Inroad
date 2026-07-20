-- Scalability indexes. Each is a defense-in-depth against a query pattern
-- that would otherwise fall back to a seq-scan or bloat unrelated pages.

-- Workspace-scoped send lookups ordered by recency (list/browse UIs).
CREATE INDEX idx_sends_workspace_created ON sends (workspace_id, created_at DESC);

-- Sweeper hot path: locate stuck 'queued' rows by age. Partial keeps it tiny.
CREATE INDEX idx_sends_queued_created ON sends (created_at) WHERE status = 'queued';

-- Session expiry sweeps ignore already-revoked rows. Partial matches the query shape.
CREATE INDEX idx_sessions_expires ON sessions (expires_at) WHERE revoked_at IS NULL;

-- Case-insensitive suppression lookup by email alone (cross-workspace scans;
-- the existing idx_suppression_ws_email covers per-workspace).
CREATE INDEX idx_suppression_email ON suppression (lower(email));
