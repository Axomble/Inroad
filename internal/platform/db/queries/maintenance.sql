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
),
-- Federated sign-in states are single-use and live ~10 minutes; a 7-day window
-- matches the other short-lived challenge artifacts above. Batched on nonce_hash
-- (the primary key) rather than an id column, which this table doesn't have.
deleted_oauth_login_states AS (
    DELETE FROM oauth_login_states
    WHERE nonce_hash IN (
        SELECT nonce_hash FROM oauth_login_states
        WHERE expires_at < now() - interval '7 days'
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
    (SELECT count(*) FROM deleted_oauth_refresh) +
    (SELECT count(*) FROM deleted_oauth_login_states)
)::bigint AS deleted_rows;

-- name: PurgeDeadWorkers :one
-- Reap worker registry rows long past the assigner's live window, together with
-- the mailbox assignments pinned to them.
--
-- The retention here (24h) is deliberately far wider than the 15m live window the
-- assigner uses. Those two windows answer different questions: 15m decides "may
-- this worker take new work", and a worker crossing it already has its mailboxes
-- reassigned on next resolve. This one decides "will this worker ever come back",
-- where a wide margin costs nothing — a stale row is inert once the assigner
-- stops trusting it — and a narrow one would churn rows for a worker restarting
-- under its own id. Deleting the assignments too keeps the least-loaded pick
-- honest: it counts assignments per worker, so rows owned by long-gone workers
-- would otherwise permanently inflate a dead worker's load and skew balancing.
--
-- workers is global infra state, not tenant data, hence no workspace pin (see
-- migration 000017). Assignments cascade-delete with their mailbox/workspace
-- already; this covers the case where both parents live on but the worker died.
WITH dead AS (
    SELECT worker_id FROM workers
    WHERE last_seen_at < now() - interval '24 hours'
    ORDER BY last_seen_at LIMIT 5000
),
deleted_assignments AS (
    DELETE FROM mailbox_worker_assignments
    WHERE worker_id IN (SELECT worker_id FROM dead)
    RETURNING 1
),
deleted_workers AS (
    DELETE FROM workers
    WHERE worker_id IN (SELECT worker_id FROM dead)
    RETURNING 1
)
SELECT (
    (SELECT count(*) FROM deleted_assignments) +
    (SELECT count(*) FROM deleted_workers)
)::bigint AS deleted_rows;

-- name: PurgeExpiredIdempotencyKeys :one
-- The Idempotency-Key replay cache (migration 000045) has its own fixed 24h
-- retention window, independent of the security-artifact purge above: once a
-- key falls out of window, a client retrying it is simply treated as a brand
-- new request, and the row is free to be reclaimed. Kept as a SEPARATE query
-- (not folded into PurgeExpiredSecurityArtifacts) because that query's own
-- doc deliberately scopes it to authentication/authorization artifacts; the
-- idempotency cache is an HTTP-layer concern, not a security artifact.
-- Batched at 5000 rows, same bound as the query above, to cap one sweep's
-- lock/IO footprint. The primary key is (workspace_id, key) rather than a
-- single id column, so the batching subquery selects that pair.
WITH deleted_idempotency_keys AS (
    DELETE FROM idempotency_keys
    WHERE (workspace_id, key) IN (
        SELECT workspace_id, key FROM idempotency_keys
        WHERE created_at < now() - interval '24 hours'
        ORDER BY created_at LIMIT 5000
    )
    RETURNING 1
)
SELECT count(*)::bigint AS deleted_rows FROM deleted_idempotency_keys;
