-- name: GetWorkspaceDEK :one
SELECT wrapped_dek, key_provider FROM workspace_deks WHERE workspace_id = $1;

-- name: CreateWorkspaceDEK :exec
-- Fail-if-exists: the PK rejects an overwrite. A DEK is never replaced in place
-- (that would silently invalidate all prior ciphertext); rotation is an explicit
-- re-encrypt path, not implemented here.
INSERT INTO workspace_deks (workspace_id, wrapped_dek, key_provider)
VALUES ($1, $2, $3);
