ALTER TABLE provider_accounts
    ADD COLUMN IF NOT EXISTS fast_mode_enabled boolean NOT NULL DEFAULT false;
