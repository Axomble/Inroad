-- name: InsertIdempotencyKey :one
-- Attempts to claim (workspace_id, key) for a new idempotent request.
-- ON CONFLICT DO NOTHING means a losing insert (the row already exists)
-- returns no row -- the store maps that pgx.ErrNoRows to inserted=false so
-- the caller falls through to GetIdempotencyKey to resolve the conflict.
INSERT INTO idempotency_keys (workspace_id, key, request_hash)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, key) DO NOTHING
RETURNING *;

-- name: GetIdempotencyKey :one
-- Loads the existing row for (workspace_id, key) to resolve a conflict: same
-- hash + response recorded -> replay; same hash + no response yet -> in
-- flight; different hash -> key reuse. Deliberately does NOT filter by age
-- (kept pure) -- purging rows older than 24h is the maintenance sweep's job.
SELECT * FROM idempotency_keys WHERE workspace_id = $1 AND key = $2;

-- name: SetIdempotencyResponse :exec
-- Persists the captured response once the wrapped handler finishes -- or the
-- uncacheable sentinel (status_code=0, response_body/content_type left NULL)
-- when the response exceeded the 64 KiB cache cap. A real HTTP status is
-- always >= 100, so 0 is unambiguous against both a genuine status and the
-- NULL "still in flight" state the row starts in.
UPDATE idempotency_keys
SET status_code = $1, response_body = $2, content_type = $3
WHERE workspace_id = $4 AND key = $5;
