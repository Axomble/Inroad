-- name: PurgeExpiredSecurityArtifacts :one
-- Keep a short forensic window after credentials become unusable, then remove
-- them in one transaction. This intentionally excludes business/audit data such
-- as sends and tracking events; those require an explicit product retention
-- policy rather than a guessed destructive default.
WITH
deleted_sessions AS (
    DELETE FROM sessions
    WHERE id IN (
        SELECT id FROM sessions
        WHERE expires_at < now() - interval '30 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_invites AS (
    DELETE FROM workspace_invites
    WHERE id IN (
        SELECT id FROM workspace_invites
        WHERE expires_at < now() - interval '30 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_two_factor AS (
    DELETE FROM two_factor_challenges
    WHERE id IN (
        SELECT id FROM two_factor_challenges
        WHERE expires_at < now() - interval '7 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_webauthn AS (
    DELETE FROM webauthn_challenges
    WHERE id IN (
        SELECT id FROM webauthn_challenges
        WHERE expires_at < now() - interval '7 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_email_otp AS (
    DELETE FROM email_otp_codes
    WHERE id IN (
        SELECT id FROM email_otp_codes
        WHERE expires_at < now() - interval '7 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_oauth_requests AS (
    DELETE FROM oauth_authorization_requests
    WHERE id IN (
        SELECT id FROM oauth_authorization_requests
        WHERE expires_at < now() - interval '7 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_oauth_codes AS (
    DELETE FROM oauth_authorization_codes
    WHERE id IN (
        SELECT id FROM oauth_authorization_codes
        WHERE expires_at < now() - interval '7 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_oauth_access AS (
    DELETE FROM oauth_access_tokens
    WHERE id IN (
        SELECT id FROM oauth_access_tokens
        WHERE expires_at < now() - interval '30 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
),
deleted_oauth_refresh AS (
    DELETE FROM oauth_refresh_tokens
    WHERE id IN (
        SELECT id FROM oauth_refresh_tokens
        WHERE expires_at < now() - interval '30 days'
        ORDER BY expires_at LIMIT 5000
    )
    RETURNING 1
)
SELECT (
    (SELECT count(*) FROM deleted_sessions) +
    (SELECT count(*) FROM deleted_invites) +
    (SELECT count(*) FROM deleted_two_factor) +
    (SELECT count(*) FROM deleted_webauthn) +
    (SELECT count(*) FROM deleted_email_otp) +
    (SELECT count(*) FROM deleted_oauth_requests) +
    (SELECT count(*) FROM deleted_oauth_codes) +
    (SELECT count(*) FROM deleted_oauth_access) +
    (SELECT count(*) FROM deleted_oauth_refresh)
)::bigint AS deleted_rows;
