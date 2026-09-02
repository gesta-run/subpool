ALTER TABLE provider_accounts
    ADD COLUMN IF NOT EXISTS reset_credits_snapshot jsonb,
    ADD COLUMN IF NOT EXISTS reset_credits_checked_at timestamptz;
