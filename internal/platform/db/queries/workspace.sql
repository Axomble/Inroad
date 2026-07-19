-- name: CreateWorkspace :one
INSERT INTO workspaces (name) VALUES ($1) RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;
