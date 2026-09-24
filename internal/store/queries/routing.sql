-- name: CreatePool :exec
INSERT INTO pools(id, name, provider)
VALUES (sqlc.arg(id), sqlc.arg(name), sqlc.arg(provider));

-- name: AddInitialPoolAccount :execrows
INSERT INTO pool_accounts(pool_id, provider_account_id, weight, priority, enabled)
SELECT sqlc.arg(pool_id), sqlc.arg(provider_account_id), sqlc.arg(weight), sqlc.arg(priority), sqlc.arg(enabled)
FROM provider_accounts a
WHERE a.id = sqlc.arg(provider_account_id);

-- name: UpdatePool :execrows
UPDATE pools
SET name = sqlc.arg(name), updated_at = now()
WHERE id = sqlc.arg(id);

-- name: ListPools :many
SELECT id, name, provider, created_at, updated_at
FROM pools
ORDER BY created_at;

-- name: ListPoolAccounts :many
SELECT pool_id, provider_account_id, weight, priority, enabled
FROM pool_accounts
ORDER BY priority, provider_account_id;

-- name: LockPoolForUpdate :one
SELECT provider FROM pools WHERE id = sqlc.arg(id) FOR UPDATE;

-- name: GetProviderAccountProvider :one
SELECT provider FROM provider_accounts WHERE id = sqlc.arg(id);

-- name: UpsertPoolAccount :execrows
INSERT INTO pool_accounts(pool_id, provider_account_id, weight, priority, enabled)
VALUES (sqlc.arg(pool_id), sqlc.arg(provider_account_id), sqlc.arg(weight), sqlc.arg(priority), sqlc.arg(enabled))
ON CONFLICT(pool_id, provider_account_id) DO UPDATE SET
    weight = excluded.weight,
    priority = excluded.priority,
    enabled = excluded.enabled;

-- name: MarkPoolMixed :exec
UPDATE pools SET provider = 'mixed', updated_at = now() WHERE id = sqlc.arg(id);

-- name: LockPoolAssignments :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(pool_id), 731242002));

-- name: SelectAccountForUpdate :one
SELECT a.id
FROM provider_accounts a
JOIN pool_accounts pa ON pa.provider_account_id = a.id
    AND pa.pool_id = sqlc.arg(pool_id)
    AND pa.enabled
CROSS JOIN global_settings settings
WHERE NOT (a.id::text = ANY(COALESCE(sqlc.arg(exclude_ids)::text[], ARRAY[]::text[])))
  AND (a.status = 'active' OR (a.status = 'cooling_down' AND a.cooldown_until <= now()))
  AND COALESCE(NULLIF(a.health_status, ''), 'unknown') != 'unhealthy'
  AND (SELECT count(*) FROM api_key_account_bindings b
      JOIN api_keys k ON k.id = b.api_key_id
      WHERE b.provider_account_id = a.id
        AND k.revoked_at IS NULL
        AND (k.expires_at IS NULL OR k.expires_at > now())) < settings.max_api_keys_per_account
ORDER BY pa.priority ASC,
    (SELECT count(*)::numeric / GREATEST(pa.weight, 1)
      FROM api_key_account_bindings b
      JOIN api_keys k ON k.id = b.api_key_id
      WHERE b.provider_account_id = a.id
        AND k.revoked_at IS NULL
        AND (k.expires_at IS NULL OR k.expires_at > now())) ASC,
    random()
FOR UPDATE OF a
LIMIT 1;

-- name: ProviderAccountHasCapacity :one
SELECT (
    SELECT count(*) FROM api_key_account_bindings b
    JOIN api_keys k ON k.id = b.api_key_id
    WHERE b.provider_account_id = sqlc.arg(provider_account_id)
      AND k.revoked_at IS NULL
      AND (k.expires_at IS NULL OR k.expires_at > now())
) < settings.max_api_keys_per_account
FROM global_settings settings
WHERE settings.singleton;

-- name: CreateEmployee :exec
INSERT INTO employees(id, name)
VALUES (sqlc.arg(id), sqlc.arg(name));

-- name: CreateAPIKey :exec
INSERT INTO api_keys(id, pool_id, employee_id, key_hmac, key_hint, scopes, rate_limit, expires_at)
VALUES (sqlc.arg(id), sqlc.arg(pool_id), sqlc.arg(employee_id), sqlc.arg(key_hmac),
    sqlc.arg(key_hint), sqlc.arg(scopes), sqlc.arg(rate_limit), sqlc.narg(expires_at));

-- name: BindAPIKey :exec
INSERT INTO api_key_account_bindings(api_key_id, provider_account_id)
VALUES (sqlc.arg(api_key_id), sqlc.arg(provider_account_id));

-- name: ListAPIKeys :many
SELECT k.id, k.pool_id, COALESCE(b.provider_account_id::text, '')::text AS provider_account_id,
    k.employee_id, e.name AS employee_name, k.key_hint, k.scopes, k.rate_limit, k.expires_at, k.revoked_at,
    k.last_used_at, k.created_at
FROM api_keys k
JOIN employees e ON e.id = k.employee_id
LEFT JOIN api_key_account_bindings b ON b.api_key_id = k.id
ORDER BY k.last_used_at DESC NULLS LAST, k.created_at DESC;

-- name: RevokeAPIKey :execrows
UPDATE api_keys SET revoked_at = now()
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: ResolveAPIKey :one
SELECT
    k.id AS key_id, k.pool_id AS key_pool_id, k.employee_id, e.name AS employee_name, k.key_hint, k.scopes,
    k.rate_limit, k.expires_at AS key_expires_at, k.revoked_at, k.last_used_at,
    k.created_at AS key_created_at,
    p.id AS pool_id, p.name AS pool_name, p.provider AS pool_provider,
    p.created_at AS pool_created_at, p.updated_at AS pool_updated_at,
    a.id AS account_id, a.provider AS account_provider, a.credential_type,
    a.display_name, a.credential_ciphertext, a.credential_version, a.status,
    a.fast_mode_enabled, a.health_status, a.quota_snapshot, a.cooldown_until,
    a.last_success_at, a.last_failure_at, a.created_at AS account_created_at,
    a.updated_at AS account_updated_at, pa.enabled AS membership_enabled
FROM api_keys k
JOIN employees e ON e.id = k.employee_id
JOIN pools p ON p.id = k.pool_id
JOIN api_key_account_bindings b ON b.api_key_id = k.id
JOIN provider_accounts a ON a.id = b.provider_account_id
JOIN pool_accounts pa ON pa.pool_id = p.id AND pa.provider_account_id = a.id
WHERE k.key_hmac = sqlc.arg(key_hmac)
  AND k.revoked_at IS NULL
  AND (k.expires_at IS NULL OR k.expires_at > now());

-- name: ResolvePinnedAPIKey :one
SELECT
    k.id AS key_id, k.pool_id AS key_pool_id, k.employee_id, e.name AS employee_name, k.key_hint, k.scopes,
    k.rate_limit, k.expires_at AS key_expires_at, k.revoked_at, k.last_used_at,
    k.created_at AS key_created_at,
    p.id AS pool_id, p.name AS pool_name, p.provider AS pool_provider,
    p.created_at AS pool_created_at, p.updated_at AS pool_updated_at,
    a.id AS account_id, a.provider AS account_provider, a.credential_type,
    a.display_name, a.credential_ciphertext, a.credential_version, a.status,
    a.fast_mode_enabled, a.health_status, a.quota_snapshot, a.cooldown_until,
    a.last_success_at, a.last_failure_at, a.created_at AS account_created_at,
    a.updated_at AS account_updated_at, pa.enabled AS membership_enabled
FROM api_keys k
JOIN employees e ON e.id = k.employee_id
JOIN pools p ON p.id = k.pool_id
JOIN pool_accounts pa ON pa.pool_id = p.id AND pa.provider_account_id = sqlc.arg(account_id)
JOIN provider_accounts a ON a.id = pa.provider_account_id
WHERE k.key_hmac = sqlc.arg(key_hmac)
  AND k.pool_id = sqlc.arg(pool_id)
  AND k.revoked_at IS NULL
  AND (k.expires_at IS NULL OR k.expires_at > now());

-- name: PinnedAPIKeyValid :one
SELECT EXISTS(
    SELECT 1 FROM api_keys
    WHERE key_hmac = sqlc.arg(key_hmac)
      AND pool_id = sqlc.arg(pool_id)
      AND revoked_at IS NULL
      AND (expires_at IS NULL OR expires_at > now())
);

-- name: ResolveSessionAccount :one
SELECT a.id, a.provider, a.credential_type, a.display_name, a.credential_ciphertext,
    a.credential_version, a.status, a.fast_mode_enabled, a.health_status,
    a.quota_snapshot, a.cooldown_until, a.last_success_at, a.last_failure_at,
    a.created_at, a.updated_at
FROM session_bindings s
JOIN provider_accounts a ON a.id = s.provider_account_id
JOIN pool_accounts pa ON pa.pool_id = s.pool_id
    AND pa.provider_account_id = a.id
    AND pa.enabled
WHERE s.api_key_id = sqlc.arg(api_key_id)
  AND s.session_hash = sqlc.arg(session_hash)
  AND s.expires_at > now();

-- name: SaveSessionBinding :exec
INSERT INTO session_bindings(api_key_id, pool_id, session_hash, provider_account_id, expires_at)
VALUES (sqlc.arg(api_key_id), sqlc.arg(pool_id), sqlc.arg(session_hash),
    sqlc.arg(provider_account_id), sqlc.arg(expires_at))
ON CONFLICT(api_key_id, session_hash) DO UPDATE
SET expires_at = GREATEST(session_bindings.expires_at, excluded.expires_at)
WHERE session_bindings.provider_account_id = excluded.provider_account_id;

-- name: ReassignAPIKey :exec
UPDATE api_key_account_bindings
SET provider_account_id = sqlc.arg(provider_account_id), assigned_at = now()
WHERE api_key_id = sqlc.arg(api_key_id);

-- name: GetRoutableProviderAccount :one
SELECT id, provider, credential_type, display_name, credential_ciphertext,
    credential_version, status, fast_mode_enabled, health_status, quota_snapshot,
    cooldown_until, last_success_at, last_failure_at, created_at, updated_at
FROM provider_accounts
WHERE id = sqlc.arg(id);

-- name: RecordRequestSuccess :exec
WITH account AS (
    UPDATE provider_accounts SET
        status = CASE WHEN status IN ('disabled', 'exhausted') THEN status ELSE 'active' END,
        cooldown_until = CASE WHEN status IN ('disabled', 'exhausted') THEN cooldown_until ELSE NULL END,
        last_success_at = sqlc.arg(occurred_at),
        health_status = 'healthy',
        last_checked_at = sqlc.arg(occurred_at),
        last_health_error_code = NULL,
        consecutive_health_failures = 0,
        updated_at = now()
    WHERE provider_accounts.id = sqlc.arg(account_id)
    RETURNING provider_accounts.id
)
UPDATE api_keys key
SET last_used_at = sqlc.arg(occurred_at)
WHERE key.id = sqlc.arg(api_key_id) AND EXISTS(SELECT 1 FROM account);

-- name: DeduplicateUsageEvent :execrows
INSERT INTO usage_event_dedup(api_key_id, event_hash)
VALUES (sqlc.arg(api_key_id), sqlc.arg(event_hash))
ON CONFLICT DO NOTHING;

-- name: AddUsage :exec
INSERT INTO api_key_usage_daily(api_key_id, usage_date, model, input_tokens, output_tokens)
VALUES (sqlc.arg(api_key_id), sqlc.arg(usage_date), sqlc.arg(model),
    sqlc.arg(input_tokens), sqlc.arg(output_tokens))
ON CONFLICT(api_key_id, usage_date, model) DO UPDATE SET
    input_tokens = api_key_usage_daily.input_tokens + excluded.input_tokens,
    output_tokens = api_key_usage_daily.output_tokens + excluded.output_tokens,
    updated_at = now();

-- name: UpdateAPIKeyActivity :exec
UPDATE api_keys
SET last_used_at = GREATEST(COALESCE(last_used_at, sqlc.arg(occurred_at)), sqlc.arg(occurred_at))
WHERE id = sqlc.arg(id);

-- name: ListUsageEmployees :many
WITH employee_totals AS (
    SELECT e.id AS employee_id, e.name AS employee_name,
        COUNT(DISTINCT u.api_key_id)::bigint AS key_count,
        COUNT(DISTINCT u.model)::bigint AS model_count,
        SUM(u.input_tokens)::bigint AS input_tokens,
        SUM(u.output_tokens)::bigint AS output_tokens,
        (SUM(u.input_tokens) + SUM(u.output_tokens))::bigint AS total_tokens
    FROM api_key_usage_daily u
    JOIN api_keys k ON k.id = u.api_key_id
    JOIN employees e ON e.id = k.employee_id
    WHERE (NULLIF(sqlc.arg(api_key_id)::text, '')::uuid IS NULL OR u.api_key_id = NULLIF(sqlc.arg(api_key_id)::text, '')::uuid)
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR u.usage_date >= sqlc.narg(from_time)::date)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR u.usage_date <= sqlc.narg(to_time)::date)
    GROUP BY e.id, e.name
)
SELECT employee_id, employee_name, key_count, model_count, input_tokens, output_tokens
FROM employee_totals
WHERE sqlc.narg(after_total)::bigint IS NULL
   OR total_tokens < sqlc.narg(after_total)::bigint
   OR (total_tokens = sqlc.narg(after_total)::bigint AND employee_id::text > sqlc.arg(after_id)::text)
ORDER BY total_tokens DESC, employee_id
LIMIT sqlc.arg(page_limit);

-- name: ListUsageDetails :many
WITH details AS (
    SELECT u.api_key_id, e.name AS employee_name, k.key_hint, u.model,
        SUM(u.input_tokens)::bigint AS input_tokens,
        SUM(u.output_tokens)::bigint AS output_tokens,
        (SUM(u.input_tokens) + SUM(u.output_tokens))::bigint AS total_tokens
    FROM api_key_usage_daily u
    JOIN api_keys k ON k.id = u.api_key_id
    JOIN employees e ON e.id = k.employee_id
    WHERE k.employee_id = sqlc.arg(employee_id)::uuid
      AND (sqlc.narg(from_time)::timestamptz IS NULL OR u.usage_date >= sqlc.narg(from_time)::date)
      AND (sqlc.narg(to_time)::timestamptz IS NULL OR u.usage_date <= sqlc.narg(to_time)::date)
    GROUP BY u.api_key_id, e.name, k.key_hint, u.model
)
SELECT api_key_id, employee_name, key_hint, model, input_tokens, output_tokens
FROM details
WHERE sqlc.narg(after_total)::bigint IS NULL
   OR total_tokens < sqlc.narg(after_total)::bigint
   OR (total_tokens = sqlc.narg(after_total)::bigint AND api_key_id::text > sqlc.arg(after_id)::text)
   OR (total_tokens = sqlc.narg(after_total)::bigint AND api_key_id::text = sqlc.arg(after_id)::text AND model > sqlc.arg(after_model)::text)
ORDER BY total_tokens DESC, api_key_id, model
LIMIT sqlc.arg(page_limit);

-- name: GetUsageTotals :one
SELECT
    COALESCE(SUM(u.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(SUM(u.output_tokens), 0)::bigint AS output_tokens
FROM api_key_usage_daily u
WHERE (NULLIF(sqlc.arg(api_key_id)::text, '')::uuid IS NULL OR u.api_key_id = NULLIF(sqlc.arg(api_key_id)::text, '')::uuid)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR u.usage_date >= sqlc.narg(from_time)::date)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR u.usage_date <= sqlc.narg(to_time)::date);

-- name: ListTopUsageKeys :many
SELECT u.api_key_id, e.name AS employee_name, k.key_hint,
    SUM(u.input_tokens)::bigint AS input_tokens,
    SUM(u.output_tokens)::bigint AS output_tokens
FROM api_key_usage_daily u
JOIN api_keys k ON k.id = u.api_key_id
JOIN employees e ON e.id = k.employee_id
WHERE (NULLIF(sqlc.arg(api_key_id)::text, '')::uuid IS NULL OR u.api_key_id = NULLIF(sqlc.arg(api_key_id)::text, '')::uuid)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR u.usage_date >= sqlc.narg(from_time)::date)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR u.usage_date <= sqlc.narg(to_time)::date)
GROUP BY u.api_key_id, e.name, k.key_hint
ORDER BY (SUM(u.input_tokens) + SUM(u.output_tokens)) DESC, u.api_key_id
LIMIT sqlc.arg(top_limit);

-- name: WriteAuditEvent :exec
INSERT INTO audit_events(actor, action, target_type, target_id, result)
VALUES (sqlc.arg(actor), sqlc.arg(action), sqlc.arg(target_type), sqlc.arg(target_id), sqlc.arg(result));
