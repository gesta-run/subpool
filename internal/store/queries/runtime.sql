-- name: LockAdminLoginScope :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(scope_key), 731242003));

-- name: GetAdminLoginFailures :one
SELECT failure_count, reset_at
FROM admin_login_failures
WHERE scope_key = sqlc.arg(scope_key);

-- name: ClearAdminLoginFailures :exec
DELETE FROM admin_login_failures
WHERE scope_key = ANY(sqlc.arg(scope_keys)::text[]);

-- name: RecordAdminLoginFailure :exec
INSERT INTO admin_login_failures(scope_key, failure_count, reset_at)
VALUES (sqlc.arg(scope_key), 1, sqlc.arg(now)::timestamptz + interval '1 minute')
ON CONFLICT(scope_key) DO UPDATE SET
    failure_count = CASE
        WHEN admin_login_failures.reset_at <= sqlc.arg(now)::timestamptz THEN 1
        ELSE admin_login_failures.failure_count + 1
    END,
    reset_at = CASE
        WHEN admin_login_failures.reset_at <= sqlc.arg(now)::timestamptz THEN sqlc.arg(now)::timestamptz + interval '1 minute'
        ELSE admin_login_failures.reset_at
    END,
    updated_at = now();

-- name: CreateAdminSession :exec
INSERT INTO admin_sessions(session_hash, expires_at)
VALUES (sqlc.arg(session_hash), sqlc.arg(expires_at))
ON CONFLICT(session_hash) DO UPDATE
SET expires_at = excluded.expires_at, revoked_at = NULL;

-- name: AdminSessionActive :one
SELECT EXISTS(
    SELECT 1 FROM admin_sessions
    WHERE session_hash = sqlc.arg(session_hash)
      AND revoked_at IS NULL
      AND expires_at > sqlc.arg(now)
);

-- name: RevokeAdminSession :exec
UPDATE admin_sessions
SET revoked_at = sqlc.arg(revoked_at)
WHERE session_hash = sqlc.arg(session_hash) AND revoked_at IS NULL;

-- name: AllowAPIKeyRequest :one
INSERT INTO api_key_rate_limits(api_key_id, window_start, request_count)
VALUES (sqlc.arg(api_key_id), date_trunc('minute', sqlc.arg(now)::timestamptz), 1)
ON CONFLICT(api_key_id) DO UPDATE SET
    window_start = CASE
        WHEN api_key_rate_limits.window_start < date_trunc('minute', sqlc.arg(now)::timestamptz)
            THEN date_trunc('minute', sqlc.arg(now)::timestamptz)
        ELSE api_key_rate_limits.window_start
    END,
    request_count = CASE
        WHEN api_key_rate_limits.window_start < date_trunc('minute', sqlc.arg(now)::timestamptz)
            THEN 1
        ELSE api_key_rate_limits.request_count + 1
    END
RETURNING request_count <= sqlc.arg(request_limit);
