-- Generic Idempotency-Key replay cache for mutating requests (POST/PUT/PATCH/
-- DELETE). Workspace-scoped: the primary key is (workspace_id, key) so two
-- workspaces can never collide on the same client-chosen key. request_hash
-- pins the exact request (method+path+body) the key was first used for, so a
-- REUSED key against a DIFFERENT request is rejected rather than silently
-- replaying the wrong response. status_code/response_body/content_type stay
-- NULL until the wrapped handler finishes -- the middleware reads a NULL
-- status_code as "still in flight" (or crashed before finishing). The read
-- path is deliberately pure (no age filter); the maintenance sweep purges
-- rows older than 24h.
CREATE TABLE idempotency_keys (
  workspace_id uuid NOT NULL,
  key          text NOT NULL CHECK (char_length(key) BETWEEN 1 AND 255),
  request_hash bytea NOT NULL,
  status_code  int,
  response_body bytea,
  content_type text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, key)
);
