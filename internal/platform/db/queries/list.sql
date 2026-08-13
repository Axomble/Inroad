-- name: CreateList :one
INSERT INTO lists (workspace_id, name) VALUES ($1, $2) RETURNING *;
-- name: GetList :one
SELECT * FROM lists WHERE id = $1 AND workspace_id = $2;
-- name: ListLists :many
-- Each list carries its membership size, so the picker can tell a 12-contact
-- list from a 40,000-contact one before a campaign is aimed at it. LEFT JOIN
-- (not INNER) so an empty list still returns a row, with 0. The count is cast
-- explicitly: an uncast aggregate generates interface{} in gen/ and still
-- compiles.
SELECT l.id, l.workspace_id, l.name, l.created_at,
       COUNT(lm.contact_id)::bigint AS contact_count
FROM lists l
LEFT JOIN list_members lm ON lm.list_id = l.id
WHERE l.workspace_id = $1
GROUP BY l.id
ORDER BY l.created_at DESC;
-- name: RenameList :one
UPDATE lists SET name = $3 WHERE id = $1 AND workspace_id = $2 RETURNING *;
-- name: DeleteList :execrows
-- list_members cascades (ON DELETE CASCADE); a list still referenced by a
-- campaign is blocked by campaigns.list_id ON DELETE RESTRICT (23503).
DELETE FROM lists WHERE id = $1 AND workspace_id = $2;
-- name: CountListMembers :one
SELECT count(*) FROM list_members WHERE list_id = $1;
