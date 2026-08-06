-- name: InsertIdempotencyKey :one
-- Attempts to claim (workspace_id, key) for a new idempotent request.
--
-- A conflict against a row STILL inside its 24h retention window falls
-- through the WHERE guard: the DO UPDATE does not fire, no row satisfies
-- RETURNING, and the store maps that pgx.ErrNoRows to inserted=false so the
-- caller falls through to GetIdempotencyKey to resolve the conflict.
--
-- A conflict against an EXPIRED row (created_at older than 24h) is reclaimed
-- atomically in this one statement -- overwritten with the new request_hash
-- and a cleared response, exactly as if the row had never existed. This is
-- the real implementation of "a matched-but-expired row is treated as
-- absent" (rule 6): it does not wait for the maintenance sweep to physically
-- delete the row first, and it reclaims regardless of whether the OLD row's
-- hash matches the new request -- an expired key is available for reuse by
-- ANY new request, not just a retry of the same one.
INSERT INTO idempotency_keys (workspace_id, key, request_hash)
VALUES ($1, $2, $3)
ON CONFLICT (workspace_id, key) DO UPDATE
SET request_hash = EXCLUDED.request_hash,
    status_code = NULL,
    response_body = NULL,
    content_type = NULL,
    created_at = now()
WHERE idempotency_keys.created_at < now() - interval '24 hours'
RETURNING *;

-- name: GetIdempotencyKey :one
-- Loads the existing row for (workspace_id, key) to resolve a conflict: same
-- hash + response recorded -> replay; same hash + no response yet -> in
-- flight; different hash -> key reuse.
--
-- The age filter is defense in depth, not the primary expiry mechanism (that
-- is InsertIdempotencyKey's atomic reclaim above): by the time a conflict
-- reaches this query, InsertIdempotencyKey has already reclaimed any row
-- older than 24h, so in practice this only ever matches a row inside its
-- window. It stays here anyway so a row that ages out in the narrow gap
-- between that INSERT and this SELECT can never be replayed with stale data.
SELECT * FROM idempotency_keys
WHERE workspace_id = $1 AND key = $2 AND created_at >= now() - interval '24 hours';

-- name: SetIdempotencyResponse :exec
-- Persists the captured response once the wrapped handler finishes -- or the
-- uncacheable sentinel (status_code=0, response_body/content_type left NULL)
-- when the response exceeded the 64 KiB cache cap. A real HTTP status is
-- always >= 100, so 0 is unambiguous against both a genuine status and the
-- NULL "still in flight" state the row starts in. Never called for a >= 500
-- response -- see DeleteIdempotencyKey.
UPDATE idempotency_keys
SET status_code = $1, response_body = $2, content_type = $3
WHERE workspace_id = $4 AND key = $5;

-- name: DeleteIdempotencyKey :exec
-- Releases the claim row after the wrapped handler returned a >= 500
-- response: a transient server error must not be locked in for 24h, either
-- as a replayed failure or (worse) a permanent idempotency_uncacheable 409,
-- so a same-key retry re-executes from scratch instead. Mirrors common
-- public-API idempotency practice (e.g. Stripe): only successful/client-error
-- outcomes (2xx/3xx/4xx) are cached; 5xx is not.
DELETE FROM idempotency_keys WHERE workspace_id = $1 AND key = $2;
