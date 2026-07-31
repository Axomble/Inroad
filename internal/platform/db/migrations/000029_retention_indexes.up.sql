-- Expired security artifacts are purged by the daily maintenance worker. Keep
-- each bounded delete index-backed so cleanup does not degrade as traffic grows.
CREATE INDEX idx_sessions_expires_at ON sessions(expires_at);
CREATE INDEX idx_workspace_invites_expires_at ON workspace_invites(expires_at);
CREATE INDEX idx_two_factor_challenges_expires_at ON two_factor_challenges(expires_at);
CREATE INDEX idx_webauthn_challenges_expires_at ON webauthn_challenges(expires_at);
CREATE INDEX idx_email_otp_codes_expires_at ON email_otp_codes(expires_at);
CREATE INDEX idx_oauth_auth_requests_expires_at ON oauth_authorization_requests(expires_at);
CREATE INDEX idx_oauth_codes_expires_at ON oauth_authorization_codes(expires_at);
CREATE INDEX idx_oauth_access_expires_at ON oauth_access_tokens(expires_at);
CREATE INDEX idx_oauth_refresh_expires_at ON oauth_refresh_tokens(expires_at);
