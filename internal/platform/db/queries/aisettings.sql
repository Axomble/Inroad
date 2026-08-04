-- AI settings + multi-cloud provider credentials + user-defined models
-- (agent-platform spec §2/§3). Every tenant statement is workspace_id-pinned
-- (security invariant 4). secret_ciphertext is selected ONLY by GetAIProvider
-- — the service-internal read behind credential updates and discovery — never
-- by the list/insert/update queries the API read paths map from, so the
-- ciphertext cannot leak by careless mapping.

-- name: GetAISettings :one
SELECT * FROM workspace_ai_settings
WHERE workspace_id = $1;

-- name: UpsertAISettings :one
-- Full-row upsert for the per-workspace settings singleton. The caller merges
-- omitted request fields over current-or-default values first, so this always
-- writes a complete row.
INSERT INTO workspace_ai_settings (
    workspace_id, default_smart_model, default_fast_model,
    enabled_model_ids, additional_instructions
) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id) DO UPDATE SET
    default_smart_model     = EXCLUDED.default_smart_model,
    default_fast_model      = EXCLUDED.default_fast_model,
    enabled_model_ids       = EXCLUDED.enabled_model_ids,
    additional_instructions = EXCLUDED.additional_instructions,
    updated_at              = now()
RETURNING *;

-- name: InsertAIProvider :one
-- Create a provider door. The one-door-per-target uniqueness is the
-- uq_workspace_ai_providers_target expression index; a duplicate surfaces as
-- SQLSTATE 23505, which the store maps to a domain sentinel (409). RETURNING
-- deliberately omits secret_ciphertext (masked by construction).
INSERT INTO workspace_ai_providers (
    workspace_id, kind, config, secret_ciphertext, key_prefix, display_name
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, workspace_id, kind, config, key_prefix, display_name,
          created_at, updated_at;

-- name: GetAIProvider :one
-- INTERNAL full-row read (includes secret_ciphertext) for the service's
-- credential-update and discovery paths. Never mapped onto a response DTO.
SELECT * FROM workspace_ai_providers
WHERE id = $1 AND workspace_id = $2;

-- name: ListAIProviders :many
-- The masked read path: deliberately does NOT select secret_ciphertext.
SELECT id, workspace_id, kind, config, key_prefix, display_name,
       created_at, updated_at
FROM workspace_ai_providers
WHERE workspace_id = $1
ORDER BY created_at, id;

-- name: UpdateAIProviderConfig :one
-- Update the non-secret half (display name + connection config). Masked
-- RETURNING; run AFTER UpdateAIProviderSecret so the returned key_prefix
-- reflects a same-request credential replacement.
UPDATE workspace_ai_providers
SET display_name = $3, config = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING id, workspace_id, kind, config, key_prefix, display_name,
          created_at, updated_at;

-- name: UpdateAIProviderSecret :execrows
-- Replace the sealed credential blob + its display prefix.
UPDATE workspace_ai_providers
SET secret_ciphertext = $3, key_prefix = $4, updated_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: DeleteAIProvider :execrows
-- Cascades to the door's workspace_ai_models rows.
DELETE FROM workspace_ai_providers
WHERE id = $1 AND workspace_id = $2;

-- name: InsertAIModel :one
-- Create a user-defined model under a provider door. SELF-ENFORCING tenancy:
-- the INSERT ... SELECT emits a row ONLY when the provider row truly belongs
-- to the workspace, so a foreign provider_id inserts zero rows and RETURNING
-- yields pgx.ErrNoRows — never attaching a model to another tenant's door
-- (belt-and-braces on top of the composite FK).
INSERT INTO workspace_ai_models (
    workspace_id, provider_id, name, label,
    context_window_tokens, max_output_tokens, supports_reasoning,
    input_cost_per_mtok, output_cost_per_mtok
)
SELECT p.workspace_id, p.id, $2, $3, $4, $5, $6, $7, $8
FROM workspace_ai_providers p
WHERE p.id = sqlc.arg(provider_id) AND p.workspace_id = $1
RETURNING *;

-- name: ListAIModels :many
-- Every user-defined model in the workspace, joined for the door's kind. The
-- join is workspace-pinned on both sides (belt-and-braces).
SELECT m.id, m.workspace_id, m.provider_id, m.name, m.label,
       m.context_window_tokens, m.max_output_tokens, m.supports_reasoning,
       m.input_cost_per_mtok, m.output_cost_per_mtok, m.created_at,
       p.kind
FROM workspace_ai_models m
JOIN workspace_ai_providers p
  ON p.id = m.provider_id AND p.workspace_id = m.workspace_id
WHERE m.workspace_id = $1
ORDER BY m.created_at, m.id;

-- name: DeleteAIModel :execrows
DELETE FROM workspace_ai_models
WHERE id = $1 AND workspace_id = $2;

-- name: GetAICatalogCache :one
-- The models.dev snapshot (deployment-global, not tenant data).
SELECT payload, fetched_at FROM ai_catalog_cache WHERE id;

-- name: UpsertAICatalogCache :exec
INSERT INTO ai_catalog_cache (id, payload, fetched_at)
VALUES (true, $1, now())
ON CONFLICT (id) DO UPDATE SET payload = EXCLUDED.payload, fetched_at = now();
