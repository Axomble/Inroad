DROP INDEX IF EXISTS idx_invites_pending_email;
DROP TABLE IF EXISTS oauth_login_states;
ALTER TABLE workspaces DROP COLUMN IF EXISTS onboarding_completed_at;
DROP TABLE IF EXISTS user_identities;

-- Deliberately NOT guarded: if any federated (password-less) account exists,
-- restoring NOT NULL fails and this migration aborts. That is the intended
-- behavior -- the alternative is deleting real user accounts to make a rollback
-- succeed. An operator who genuinely needs to roll back past this point must
-- decide what happens to those accounts first.
ALTER TABLE users ALTER COLUMN password_hash SET NOT NULL;
