UPDATE api_keys k
SET last_used_at = usage.last_used_at
FROM (
    SELECT api_key_id, MAX(updated_at) AS last_used_at
    FROM api_key_usage_daily
    GROUP BY api_key_id
) usage
WHERE k.id = usage.api_key_id
  AND (k.last_used_at IS NULL OR k.last_used_at < usage.last_used_at);

CREATE INDEX IF NOT EXISTS api_keys_last_used_at_idx
    ON api_keys(last_used_at DESC NULLS LAST, created_at DESC);
