-- name: UpsertContact :one
INSERT INTO contacts (workspace_id, email, first_name, last_name, company)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, lower(email))
DO UPDATE SET first_name = EXCLUDED.first_name
RETURNING id, (xmax = 0)::boolean AS inserted;
-- name: AddListMember :exec
INSERT INTO list_members (list_id, contact_id) VALUES ($1, $2)
ON CONFLICT (list_id, contact_id) DO NOTHING;
-- name: ListContactsByList :many
SELECT c.* FROM contacts c
JOIN list_members lm ON lm.contact_id = c.id
WHERE lm.list_id = $1 AND c.workspace_id = $2
ORDER BY c.created_at DESC
LIMIT $3 OFFSET $4;
