-- Dropping loses workspace AI preferences, sealed provider credentials, and
-- user-defined model rows; credentials are operator-supplied and re-enterable,
-- the catalog cache refetches on demand, and nothing on the send path reads
-- these tables.
DROP TABLE IF EXISTS ai_catalog_cache;
DROP TABLE IF EXISTS workspace_ai_models;
DROP TABLE IF EXISTS workspace_ai_providers;
DROP TABLE IF EXISTS workspace_ai_settings;
