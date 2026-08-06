-- AI settings, multi-cloud provider credentials, user-defined models, and the
-- models.dev catalog cache for the in-app agent (agent-platform spec §2/§3).
--
-- workspace_ai_settings is a one-row-per-workspace settings record (PK =
-- workspace_id, a per-tenant singleton). The model defaults store the
-- SENTINELS 'default-smart-model' / 'default-fast-model' by default; an
-- explicit model id ("<provider_row_id>/<name>") replaces the sentinel.
-- enabled_model_ids empty means "every model of a configured provider".
CREATE TABLE workspace_ai_settings (
    workspace_id            UUID PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    default_smart_model     TEXT NOT NULL DEFAULT 'default-smart-model',
    default_fast_model      TEXT NOT NULL DEFAULT 'default-fast-model',
    enabled_model_ids       TEXT[] NOT NULL DEFAULT '{}',
    additional_instructions TEXT NOT NULL DEFAULT '',
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One row per configured LLM provider DOOR per workspace (multi-cloud: the
-- same Anthropic model may be reachable natively, via Bedrock, and via Vertex
-- — three rows, three kinds). The credential OBJECT (shape varies by kind:
-- {api_key} | {access_key_id, secret_access_key} | {service_account_json}) is
-- JSON-encoded and sealed WHOLE via the per-workspace DEK keyring
-- (crypto.Keyring.SealerFor), exactly like mailboxes.secret_ciphertext —
-- never stored or returned in plaintext (security invariants 1/2/14).
--
-- config holds only NON-SECRET connection settings (base_url, region,
-- project_id, api_version, endpoint) so it can be listed and edited freely.
--
-- key_prefix is a display identifier captured at write time (first 8 chars of
-- the api_key or access_key_id, or the service account's client_email):
-- identifies the credential without revealing it, and keeps every read path
-- free of decryption.
CREATE TABLE workspace_ai_providers (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    kind              TEXT NOT NULL CHECK (kind IN (
                        'anthropic','bedrock','vertex_anthropic','openai',
                        'azure_openai','openai_compatible','google','vertex_google')),
    config            JSONB NOT NULL DEFAULT '{}',
    secret_ciphertext TEXT NOT NULL,
    key_prefix        TEXT NOT NULL DEFAULT '',
    display_name      TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Composite-FK target so child tables can make cross-tenant references
    -- unrepresentable (the migration-000028 pattern).
    UNIQUE (id, workspace_id)
);

-- One door per distinct target: a workspace may register many
-- openai_compatible gateways (unique on base_url), many azure resources
-- (unique on endpoint), many vertex projects (unique on project_id) — but not
-- the same one twice. Expression index (not a plain constraint) because the
-- discriminating fields live inside config.
CREATE UNIQUE INDEX uq_workspace_ai_providers_target ON workspace_ai_providers (
    workspace_id, kind,
    COALESCE(config->>'base_url', ''),
    COALESCE(config->>'endpoint', ''),
    COALESCE(config->>'project_id', '')
);

-- User-defined models (gateway posture): what a provider row can actually
-- serve — an OpenRouter/LiteLLM/Ollama model, an Azure deployment, a Bedrock
-- model id — created via discovery or manual entry. Cost columns are
-- informational display metadata and nullable (many endpoints don't report
-- pricing). The composite tenant FK makes a cross-tenant provider reference
-- unrepresentable rather than merely rejected; ON DELETE CASCADE removes a
-- door's models with the door.
CREATE TABLE workspace_ai_models (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id          UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    provider_id           UUID NOT NULL,
    name                  TEXT NOT NULL,
    label                 TEXT NOT NULL,
    context_window_tokens INT  NOT NULL CHECK (context_window_tokens > 0),
    max_output_tokens     INT  NOT NULL CHECK (max_output_tokens > 0),
    supports_reasoning    BOOLEAN NOT NULL DEFAULT false,
    input_cost_per_mtok   DOUBLE PRECISION,
    output_cost_per_mtok  DOUBLE PRECISION,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (provider_id, workspace_id)
        REFERENCES workspace_ai_providers (id, workspace_id) ON DELETE CASCADE,
    UNIQUE (workspace_id, provider_id, name)
);

-- Deployment-wide cache of the models.dev registry (https://models.dev/api.json),
-- the runtime source of native-provider model metadata (NO shipped catalog
-- file). Single row (boolean-PK trick), 24h TTL enforced in code with
-- serve-stale-on-failure: an offline self-host keeps its last-known-good copy.
-- Global infrastructure state, not tenant data — never returned raw on a
-- tenant-facing API.
CREATE TABLE ai_catalog_cache (
    id         BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    payload    JSONB NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
