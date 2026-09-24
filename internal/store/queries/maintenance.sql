-- name: DeleteExpiredUsageDedup :execrows
WITH expired AS (
    SELECT usage_event_dedup.api_key_id, usage_event_dedup.event_hash
    FROM usage_event_dedup
    WHERE usage_event_dedup.created_at < sqlc.arg(cutoff)
    ORDER BY usage_event_dedup.created_at
    LIMIT sqlc.arg(batch_limit)
)
DELETE FROM usage_event_dedup target
USING expired
WHERE target.api_key_id = expired.api_key_id
  AND target.event_hash = expired.event_hash;

-- name: DeleteExpiredSessionBindings :exec
DELETE FROM session_bindings WHERE expires_at < now();

-- name: DeleteExpiredAdminSessions :exec
DELETE FROM admin_sessions WHERE expires_at < now();

-- name: DeleteExpiredAdminLoginFailures :exec
DELETE FROM admin_login_failures WHERE reset_at < now();
