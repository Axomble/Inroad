-- name: CreateList :one
INSERT INTO lists (workspace_id, name) VALUES ($1, $2) RETURNING *;
-- name: GetList :one
SELECT * FROM lists WHERE id = $1 AND workspace_id = $2;
-- name: ListLists :many
SELECT * FROM lists WHERE workspace_id = $1 ORDER BY created_at DESC;
-- name: RenameList :one
UPDATE lists SET name = $3 WHERE id = $1 AND workspace_id = $2 RETURNING *;
-- name: DeleteList :execrows
-- list_members cascades (ON DELETE CASCADE); a list still referenced by a
-- campaign is blocked by campaigns.list_id ON DELETE RESTRICT (23503).
DELETE FROM lists WHERE id = $1 AND workspace_id = $2;
-- name: CountListMembers :one
SELECT count(*) FROM list_members WHERE list_id = $1;
