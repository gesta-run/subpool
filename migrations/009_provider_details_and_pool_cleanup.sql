ALTER TABLE provider_accounts
    ADD COLUMN IF NOT EXISTS email text;

ALTER TABLE pools
    DROP COLUMN IF EXISTS model_allowlist;
