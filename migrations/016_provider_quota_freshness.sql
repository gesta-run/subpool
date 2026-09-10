ALTER TABLE provider_accounts
    ADD COLUMN IF NOT EXISTS quota_checked_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_quota_error_code text;

UPDATE provider_accounts
SET quota_checked_at = last_checked_at
WHERE quota_checked_at IS NULL
  AND quota_snapshot <> '{}'::jsonb;
