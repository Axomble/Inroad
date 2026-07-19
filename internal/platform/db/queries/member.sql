-- name: CreateMember :one
INSERT INTO workspace_members (workspace_id, user_id, role)
VALUES ($1, $2, $3) RETURNING *;

-- name: GetMember :one
SELECT * FROM workspace_members WHERE workspace_id = $1 AND user_id = $2;

-- name: ListMembersByUser :many
SELECT m.*, w.name AS workspace_name
FROM workspace_members m
JOIN workspaces w ON w.id = m.workspace_id
WHERE m.user_id = $1
ORDER BY m.last_seen_at DESC NULLS LAST, m.created_at ASC;

-- name: TouchMemberLastSeen :exec
UPDATE workspace_members SET last_seen_at = now()
WHERE workspace_id = $1 AND user_id = $2;
