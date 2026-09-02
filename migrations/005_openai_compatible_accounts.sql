ALTER TABLE provider_accounts
    DROP CONSTRAINT IF EXISTS provider_accounts_provider_check;
ALTER TABLE provider_accounts
    ADD CONSTRAINT provider_accounts_provider_check
    CHECK (provider IN ('codex', 'openai_compatible'));

ALTER TABLE provider_accounts
    DROP CONSTRAINT IF EXISTS provider_accounts_credential_type_check;
ALTER TABLE provider_accounts
    ADD CONSTRAINT provider_accounts_credential_type_check
    CHECK (credential_type IN ('subscription_oauth', 'api_key'));

ALTER TABLE pools
    DROP CONSTRAINT IF EXISTS pools_provider_check;
ALTER TABLE pools
    ADD CONSTRAINT pools_provider_check
    CHECK (provider IN ('codex', 'openai_compatible'));
