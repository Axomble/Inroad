-- name: CreateInvite :one
INSERT INTO workspace_invites (workspace_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1,$2,$3,$4,$5,$6) RETURNING *;

-- name: GetInviteByHash :one
SELECT * FROM workspace_invites WHERE token_hash = $1;

-- name: ListPendingInvites :many
SELECT * FROM workspace_invites WHERE workspace_id = $1 AND status = 'pending' ORDER BY created_at DESC;

-- name: RevokeInvite :exec
UPDATE workspace_invites SET status = 'revoked'
WHERE id = $1 AND workspace_id = $2 AND status = 'pending';

-- name: MarkInviteAccepted :exec
UPDATE workspace_invites SET status = 'accepted', accepted_at = now() WHERE id = $1;

-- name: GetPendingInviteForEmail :one
SELECT * FROM workspace_invites WHERE workspace_id = $1 AND email = $2 AND status = 'pending';
