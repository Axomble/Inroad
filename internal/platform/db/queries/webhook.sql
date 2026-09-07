-- Outbound webhooks (migration 20260907132955). Every tenant-facing statement is
-- workspace-pinned: an endpoint carries a signing secret and a delivery carries
-- the exact body sent to a receiver, so a missing pin here would leak one
-- tenant's integration config or event stream to another.
--
-- The one unpinned statement is the retention purge at the bottom, which is
-- deployment maintenance rather than a tenant read; its own comment says why.

-- name: ListWebhookEndpoints :many
-- Every endpoint in a workspace, oldest first (stable order for the settings
-- list). Also the source for the dispatch fan-out, which filters to active +
-- subscribed in Go.
SELECT * FROM webhook_endpoints
WHERE workspace_id = @workspace_id
ORDER BY created_at ASC, id ASC;

-- name: GetWebhookEndpoint :one
SELECT * FROM webhook_endpoints
WHERE workspace_id = @workspace_id AND id = @id;

-- name: CreateWebhookEndpoint :one
INSERT INTO webhook_endpoints (workspace_id, url, description, secret_ciphertext, event_types, active)
VALUES (@workspace_id, @url, @description, @secret_ciphertext, @event_types, @active)
RETURNING *;

-- name: UpdateWebhookEndpoint :one
-- PATCH semantics: each nullable arg left NULL keeps the stored value. An
-- event_types of '{}' (a non-NULL empty array) is a real change — "subscribe to
-- everything" — and is distinct from NULL ("leave the subscription list alone").
UPDATE webhook_endpoints
SET url         = COALESCE(sqlc.narg('url')::text, url),
    description  = COALESCE(sqlc.narg('description')::text, description),
    event_types = COALESCE(sqlc.narg('event_types')::text[], event_types),
    active      = COALESCE(sqlc.narg('active')::boolean, active),
    updated_at  = now()
WHERE workspace_id = @workspace_id AND id = @id
RETURNING *;

-- name: RotateWebhookEndpointSecret :one
-- Replaces the sealed signing secret. The caller mints a fresh random secret,
-- seals it under the workspace DEK, and returns the plaintext to the operator
-- exactly once.
UPDATE webhook_endpoints
SET secret_ciphertext = @secret_ciphertext, updated_at = now()
WHERE workspace_id = @workspace_id AND id = @id
RETURNING *;

-- name: DeleteWebhookEndpoint :execrows
DELETE FROM webhook_endpoints
WHERE workspace_id = @workspace_id AND id = @id;

-- name: CreateWebhookDelivery :one
-- One queued delivery. The id is supplied by the caller (not left to the column
-- default) so the JSON body — which embeds its own delivery id — can be built
-- before the row is inserted, keeping the stored payload byte-identical to what
-- is POSTed. next_attempt_at is set to now() so a delivery whose asynq enqueue is
-- lost is still visible to a reconcile sweep (idx_webhook_deliveries_due).
INSERT INTO webhook_deliveries (id, endpoint_id, workspace_id, event_type, payload, next_attempt_at)
VALUES (@id, @endpoint_id, @workspace_id, @event_type, @payload, now())
RETURNING *;

-- name: GetWebhookDelivery :one
SELECT * FROM webhook_deliveries
WHERE workspace_id = @workspace_id AND id = @id;

-- name: GetWebhookDeliveryForSend :one
-- The worker's load: one delivery plus the receiver URL and sealed secret from
-- its endpoint, workspace-pinned. The secret is decrypted control-plane-side
-- (inprocess) before it crosses the coreapi seam — the worker never opens the
-- keyring (docs/security.md invariant 1's execution-plane rule).
SELECT
    d.id                    AS delivery_id,
    d.endpoint_id           AS endpoint_id,
    d.workspace_id          AS workspace_id,
    d.event_type            AS event_type,
    d.payload               AS payload,
    d.status                AS status,
    d.attempts              AS attempts,
    e.url                   AS endpoint_url,
    e.active                AS endpoint_active,
    e.secret_ciphertext     AS endpoint_secret_ciphertext
FROM webhook_deliveries d
JOIN webhook_endpoints e ON e.id = d.endpoint_id
WHERE d.workspace_id = @workspace_id AND d.id = @id;

-- name: ListWebhookDeliveries :many
-- One endpoint's delivery log, newest first, KEYSET-paginated on (created_at, id).
-- The workspace pin is first and unconditional, outside the seek guard: the
-- cursor is caller-supplied and a tenant filter any caller value can switch off
-- is not a tenant filter (the same rule as ListTaskDeadLetters).
SELECT * FROM webhook_deliveries
WHERE workspace_id = @workspace_id
  AND endpoint_id = @endpoint_id
  AND (@seek::bool = false
       OR (created_at, id) < (@cursor_time::timestamptz, @cursor_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @page_limit;

-- name: MarkWebhookDeliveryDelivered :execrows
-- Terminal success. Guarded on status='pending' so a retried asynq job that
-- races a finalize (or acts on an already-terminal row) is a clean no-op.
UPDATE webhook_deliveries
SET status = 'delivered', attempts = @attempts, response_status = @response_status::int,
    last_error = '', next_attempt_at = NULL, delivered_at = now()
WHERE workspace_id = @workspace_id AND id = @id AND status = 'pending';

-- name: MarkWebhookDeliveryRetrying :execrows
-- A failed attempt with retries left. Stays 'pending'; next_attempt_at carries
-- the backoff schedule's next due time. response_status is NULL for a
-- transport-level failure (no HTTP response).
UPDATE webhook_deliveries
SET attempts = @attempts, last_error = @last_error,
    response_status = sqlc.narg('response_status')::int,
    next_attempt_at = @next_attempt_at::timestamptz
WHERE workspace_id = @workspace_id AND id = @id AND status = 'pending';

-- name: MarkWebhookDeliveryFailed :execrows
-- Terminal failure (retries exhausted). Same 'pending' guard as the success path.
UPDATE webhook_deliveries
SET status = 'failed', attempts = @attempts, last_error = @last_error,
    response_status = sqlc.narg('response_status')::int, next_attempt_at = NULL
WHERE workspace_id = @workspace_id AND id = @id AND status = 'pending';

-- name: PurgeWebhookDeliveries :one
-- Bound the delivery log. webhook_deliveries grows one row per (event, endpoint)
-- and is never deleted by the application, so it needs a retention sweep like
-- task_dead_letters and warmup_observations (invariant 55's reasoning). 30 days
-- is well beyond any "why didn't my integration fire" investigation window.
--
-- Batched at 5000 rows like every other purge in queries/maintenance.sql, oldest
-- first so repeated sweeps make monotonic progress. Global (no workspace pin):
-- retention is deployment maintenance, not a tenant read, and it removes rows by
-- age alone and returns only a count.
WITH deleted AS (
    DELETE FROM webhook_deliveries
    WHERE id IN (
        SELECT id FROM webhook_deliveries
        WHERE created_at < now() - interval '30 days'
        ORDER BY created_at LIMIT 5000
    )
    RETURNING 1
)
SELECT count(*)::bigint AS deleted_rows FROM deleted;
