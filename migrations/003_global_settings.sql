CREATE TABLE IF NOT EXISTS global_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    max_api_keys_per_account integer NOT NULL DEFAULT 3 CHECK (max_api_keys_per_account BETWEEN 1 AND 100),
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO global_settings(singleton, max_api_keys_per_account)
VALUES (true, 3)
ON CONFLICT (singleton) DO NOTHING;
