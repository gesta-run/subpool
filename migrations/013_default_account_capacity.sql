ALTER TABLE global_settings
    ALTER COLUMN max_api_keys_per_account SET DEFAULT 2;

UPDATE global_settings
SET max_api_keys_per_account = 2,
    updated_at = now()
WHERE singleton;
