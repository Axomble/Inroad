-- name: AddSuppression :exec
INSERT INTO suppression (workspace_id, email, reason) VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, lower(email)) DO NOTHING;
-- name: IsSuppressed :one
SELECT EXISTS (SELECT 1 FROM suppression WHERE workspace_id = $1 AND lower(email) = lower($2));
