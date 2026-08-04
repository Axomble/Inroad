-- Generic Idempotency-Key replay cache for mutating requests (POST/PUT/PATCH/
-- DELETE). Workspace-scoped: the primary key is (workspace_id, key) so two
-- workspaces can never collide on the same client-chosen key. request_hash
-- pins the exact request (method+path+body) the key was first used for, so a
-- REUSED key against a DIFFERENT request is rejected rather than silently
-- replaying the wrong response. status_code/response_body/content_type stay
-- NULL until the wrapped handler finishes -- the middleware reads a NULL
-- status_code as "still in flight" (or crashed before finishing). A row past
-- its 24h retention is reclaimed atomically the next time its key is used
-- (InsertIdempotencyKey's ON CONFLICT ... DO UPDATE ... WHERE created_at <
-- now() - interval '24 hours') rather than waiting on the maintenance sweep,
-- which is the physical storage reclaimer (frees the row even if the key is
-- never reused).
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

-- The maintenance sweep's PurgeExpiredIdempotencyKeys deletes globally by
-- created_at (not scoped to one workspace, so the (workspace_id, key)
-- primary key can't serve it) -- without this index each sweep tick is a
-- full table scan for the batch of expired rows.
CREATE INDEX idx_idempotency_keys_created_at ON idempotency_keys (created_at);
