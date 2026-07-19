-- name: CreateList :one
INSERT INTO lists (workspace_id, name) VALUES ($1, $2) RETURNING *;
-- name: GetList :one
SELECT * FROM lists WHERE id = $1 AND workspace_id = $2;
-- name: ListLists :many
SELECT * FROM lists WHERE workspace_id = $1 ORDER BY created_at DESC;
-- name: CountListMembers :one
SELECT count(*) FROM list_members WHERE list_id = $1;
