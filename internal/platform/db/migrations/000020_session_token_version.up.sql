-- token_version is bumped on a security event (password reset, and later 2FA/
-- passkey changes) so every access token minted for a session before the bump
-- is rejected by the store-backed verifier without waiting out the access TTL.
-- Existing sessions start at 0, matching the `tv` claim minted for tokens
-- issued before this migration.
ALTER TABLE sessions ADD COLUMN token_version INT NOT NULL DEFAULT 0;
