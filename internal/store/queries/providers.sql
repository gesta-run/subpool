-- name: CreateProviderAccount :exec
INSERT INTO provider_accounts (
    id, provider, credential_type, display_name, email, subject_hmac,
    credential_ciphertext, credential_version, status, quota_snapshot,
    quota_checked_at, last_quota_error_code, health_status, last_checked_at,
    last_health_error_code, consecutive_health_failures, next_health_check_at
) VALUES (
    sqlc.arg(id), sqlc.arg(provider), sqlc.arg(credential_type), sqlc.arg(display_name),
    NULLIF(sqlc.arg(email)::text, ''), sqlc.arg(subject_hmac), sqlc.arg(credential_ciphertext),
    sqlc.arg(credential_version), sqlc.arg(status), sqlc.arg(quota_snapshot),
    sqlc.narg(quota_checked_at), NULLIF(sqlc.arg(last_quota_error_code)::text, ''),
    COALESCE(NULLIF(sqlc.arg(health_status)::text, ''), 'unknown'), sqlc.narg(last_checked_at),
    NULLIF(sqlc.arg(last_health_error_code)::text, ''), sqlc.arg(consecutive_health_failures),
    sqlc.narg(next_health_check_at)
);

-- name: ListProviderAccounts :many
SELECT
    a.id, a.provider, a.credential_type, a.display_name, COALESCE(a.email, '') AS email,
    a.credential_version, a.status, a.fast_mode_enabled,
    (SELECT count(*) FROM api_key_account_bindings b
        JOIN api_keys k ON k.id = b.api_key_id
        WHERE b.provider_account_id = a.id
          AND k.revoked_at IS NULL
          AND (k.expires_at IS NULL OR k.expires_at > now())) AS assigned_api_keys,
    a.quota_snapshot, a.quota_checked_at, COALESCE(a.last_quota_error_code, '') AS last_quota_error_code,
    a.cooldown_until, a.last_success_at, a.last_failure_at, a.health_status, a.last_checked_at,
    COALESCE(a.last_health_error_code, '') AS last_health_error_code,
    a.consecutive_health_failures, a.next_health_check_at, a.created_at, a.updated_at
FROM provider_accounts a
ORDER BY a.created_at;

-- name: ListPoolProviderAccounts :many
SELECT
    a.id, a.provider, a.credential_type, a.display_name, COALESCE(a.email, '') AS email,
    a.credential_ciphertext, a.credential_version, a.status, a.fast_mode_enabled,
    a.health_status, a.quota_snapshot, a.cooldown_until, a.last_success_at,
    a.last_failure_at, a.created_at, a.updated_at
FROM provider_accounts a
JOIN pool_accounts pa ON pa.provider_account_id = a.id
WHERE pa.pool_id = sqlc.arg(pool_id)
  AND pa.enabled
  AND (a.status = 'active' OR (a.status = 'cooling_down' AND a.cooldown_until <= now()))
  AND COALESCE(NULLIF(a.health_status, ''), 'unknown') != 'unhealthy'
ORDER BY pa.priority, a.id;

-- name: GetProviderAccount :one
SELECT
    id, provider, credential_type, display_name, COALESCE(email, '') AS email,
    credential_ciphertext, credential_version, status, fast_mode_enabled,
    quota_snapshot, quota_checked_at, COALESCE(last_quota_error_code, '') AS last_quota_error_code,
    cooldown_until, last_success_at, last_failure_at, health_status, last_checked_at,
    COALESCE(last_health_error_code, '') AS last_health_error_code,
    consecutive_health_failures, next_health_check_at, created_at, updated_at
FROM provider_accounts
WHERE id = sqlc.arg(id);

-- name: UpdateProviderDetailsWithQuota :execrows
UPDATE provider_accounts SET
    email = COALESCE(NULLIF(sqlc.arg(email)::text, ''), email),
    quota_snapshot = sqlc.arg(quota_snapshot),
    quota_checked_at = sqlc.arg(quota_checked_at),
    last_quota_error_code = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: UpdateProviderEmail :execrows
UPDATE provider_accounts SET
    email = COALESCE(NULLIF(sqlc.arg(email)::text, ''), email),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetProviderQuotaError :execrows
UPDATE provider_accounts SET
    last_quota_error_code = NULLIF(sqlc.arg(error_code)::text, ''),
    next_health_check_at = sqlc.arg(next_check_at),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: GetProviderResetCredits :one
SELECT reset_credits_snapshot, reset_credits_checked_at
FROM provider_accounts
WHERE id = sqlc.arg(id);

-- name: SetProviderResetCredits :execrows
UPDATE provider_accounts SET
    reset_credits_snapshot = sqlc.arg(snapshot),
    reset_credits_checked_at = sqlc.arg(checked_at),
    reset_credits_refresh_claimed_until = NULL,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ClaimProviderResetCreditRefresh :execrows
UPDATE provider_accounts SET reset_credits_refresh_claimed_until = sqlc.arg(claimed_until)
WHERE id = sqlc.arg(id)
  AND (reset_credits_checked_at IS NULL OR reset_credits_checked_at < sqlc.arg(stale_before))
  AND (reset_credits_refresh_claimed_until IS NULL OR reset_credits_refresh_claimed_until < now());

-- name: ReleaseProviderResetCreditRefresh :execrows
UPDATE provider_accounts
SET reset_credits_refresh_claimed_until = NULL
WHERE id = sqlc.arg(id);

-- name: UpdateProviderAccount :execrows
UPDATE provider_accounts SET
    display_name = COALESCE(sqlc.narg(display_name), display_name),
    status = COALESCE(sqlc.narg(status), status),
    fast_mode_enabled = COALESCE(sqlc.narg(fast_mode_enabled), fast_mode_enabled),
    cooldown_until = CASE WHEN sqlc.narg(status) = 'active' THEN NULL ELSE cooldown_until END,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: GetSettings :one
SELECT max_api_keys_per_account, updated_at
FROM global_settings
WHERE singleton;

-- name: UpdateSettings :execrows
UPDATE global_settings
SET max_api_keys_per_account = sqlc.arg(max_api_keys_per_account), updated_at = now()
WHERE singleton;

-- name: DeleteProviderAccount :execrows
DELETE FROM provider_accounts a
WHERE a.id = sqlc.arg(id)
  AND NOT EXISTS (
      SELECT 1 FROM api_key_account_bindings b
      JOIN api_keys k ON k.id = b.api_key_id
      WHERE b.provider_account_id = a.id
        AND k.revoked_at IS NULL
        AND (k.expires_at IS NULL OR k.expires_at > now())
  );

-- name: LockProviderAccountForDeletion :one
SELECT id FROM provider_accounts WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: UpdateProviderCredentialCAS :execrows
UPDATE provider_accounts SET
    credential_ciphertext = sqlc.arg(credential_ciphertext),
    credential_version = sqlc.arg(credential_version),
    status = CASE WHEN status IN ('disabled', 'exhausted') THEN status ELSE 'active' END,
    cooldown_until = CASE WHEN status IN ('disabled', 'exhausted') THEN cooldown_until ELSE NULL END,
    updated_at = now()
WHERE id = sqlc.arg(id) AND credential_version = sqlc.arg(expected_version);

-- name: UpdateProviderStatus :execrows
UPDATE provider_accounts SET
    status = sqlc.arg(status),
    cooldown_until = sqlc.narg(cooldown_until),
    last_failure_at = CASE WHEN sqlc.arg(status) = 'active' THEN last_failure_at ELSE now() END,
    health_status = CASE WHEN sqlc.arg(status) = 'auth_failed' THEN 'unhealthy' WHEN sqlc.arg(status) = 'cooling_down' THEN 'healthy' ELSE health_status END,
    last_checked_at = CASE WHEN sqlc.arg(status) IN ('auth_failed', 'cooling_down') THEN now() ELSE last_checked_at END,
    last_health_error_code = CASE WHEN sqlc.arg(status) = 'auth_failed' THEN 'authentication_failed' WHEN sqlc.arg(status) = 'cooling_down' THEN NULL ELSE last_health_error_code END,
    consecutive_health_failures = CASE WHEN sqlc.arg(status) = 'auth_failed' THEN 3 WHEN sqlc.arg(status) = 'cooling_down' THEN 0 ELSE consecutive_health_failures END,
    next_health_check_at = CASE WHEN sqlc.arg(status) IN ('auth_failed', 'cooling_down') THEN now() + interval '5 minutes' ELSE next_health_check_at END,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetProviderUsageAllowed :execrows
UPDATE provider_accounts SET
    status = CASE
        WHEN sqlc.arg(allowed)::boolean AND status = 'exhausted' THEN 'active'
        WHEN NOT sqlc.arg(allowed)::boolean AND status IN ('active', 'cooling_down', 'exhausted') THEN 'exhausted'
        ELSE status
    END,
    cooldown_until = CASE
        WHEN (sqlc.arg(allowed)::boolean AND status = 'exhausted')
          OR (NOT sqlc.arg(allowed)::boolean AND status IN ('active', 'cooling_down', 'exhausted')) THEN NULL
        ELSE cooldown_until
    END,
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: SetProviderHealth :execrows
UPDATE provider_accounts SET
    health_status = sqlc.arg(health_status),
    last_checked_at = sqlc.arg(checked_at),
    last_health_error_code = NULLIF(sqlc.arg(error_code)::text, ''),
    consecutive_health_failures = CASE WHEN sqlc.arg(health_status) IN ('healthy', 'unknown') THEN 0 ELSE consecutive_health_failures END,
    next_health_check_at = sqlc.arg(next_check_at),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: RecordProviderHealthFailure :execrows
UPDATE provider_accounts SET
    consecutive_health_failures = consecutive_health_failures + 1,
    health_status = CASE WHEN consecutive_health_failures + 1 >= 3 THEN 'unhealthy' ELSE health_status END,
    last_checked_at = sqlc.arg(checked_at),
    last_health_error_code = sqlc.arg(error_code),
    next_health_check_at = sqlc.arg(next_check_at),
    updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ReactivateProviderIfCooldownExpired :execrows
UPDATE provider_accounts SET status = 'active', cooldown_until = NULL, updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'cooling_down'
  AND cooldown_until <= sqlc.arg(now);

-- name: ClaimProviderHealthChecks :many
WITH due AS (
    SELECT candidate.id FROM provider_accounts candidate
    WHERE (candidate.status IN ('active', 'exhausted') AND (candidate.next_health_check_at IS NULL OR candidate.next_health_check_at <= sqlc.arg(now)))
       OR (candidate.status = 'cooling_down' AND candidate.cooldown_until <= sqlc.arg(now))
    ORDER BY candidate.next_health_check_at NULLS FIRST, candidate.id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg(claim_limit)
)
UPDATE provider_accounts a
SET next_health_check_at = sqlc.arg(claimed_until), updated_at = now()
FROM due
WHERE a.id = due.id
RETURNING a.id, a.provider, a.credential_type, a.display_name, a.credential_ciphertext,
    a.credential_version, a.status, a.health_status, a.last_checked_at,
    COALESCE(a.last_health_error_code, '') AS last_health_error_code,
    a.consecutive_health_failures, a.next_health_check_at;
