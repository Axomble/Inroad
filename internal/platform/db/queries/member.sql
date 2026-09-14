-- name: CreateMember :one
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES ($1, $2, $3) RETURNING *;

-- name: GetMember :one
SELECT * FROM workspace_members WHERE workspace_id = $1 AND user_id = $2;

-- name: ListMembersByUser :many
-- w.onboarding_completed_at rides along so every auth response can tell the SPA,
-- per workspace, whether onboarding is still pending -- switching into a freshly
-- created workspace then needs no extra round trip.
SELECT m.*, w.name AS workspace_name, w.onboarding_completed_at
FROM workspace_members m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1
ORDER BY m.last_seen_at DESC NULLS LAST, m.created_at ASC;

-- name: TouchMemberLastSeen :exec
UPDATE workspace_members SET last_seen_at = now()
WHERE workspace_id = $1 AND user_id = $2;

-- name: UpsertMemberRole :one
-- Add the user to the workspace at role, or update their existing role if
-- they are already a member — the primitive `inroadctl grant-role` uses to
-- restore a lost owner, whichever way the access was lost (dropped from the
-- workspace entirely, or merely demoted). workspace_id/user_id are both FKs;
-- a foreign id fails the INSERT with 23503 rather than silently doing nothing,
-- which the caller checks for up front (GetWorkspace/GetUserByEmail) so it can
-- report which one was unknown.
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role
RETURNING *;
