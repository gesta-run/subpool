ALTER TABLE global_settings
    RENAME COLUMN max_api_keys_per_account TO max_concurrent_requests_per_account;

ALTER TABLE provider_accounts
    DROP COLUMN IF EXISTS max_api_keys;
