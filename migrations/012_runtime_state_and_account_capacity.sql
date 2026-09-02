ALTER TABLE global_settings
    RENAME COLUMN max_concurrent_requests_per_account TO max_api_keys_per_account;

ALTER TABLE pools
    DROP COLUMN IF EXISTS strategy;

ALTER TABLE session_bindings
    DROP CONSTRAINT IF EXISTS session_bindings_provider_account_id_fkey;

ALTER TABLE session_bindings
    ADD CONSTRAINT session_bindings_provider_account_id_fkey
    FOREIGN KEY (provider_account_id) REFERENCES provider_accounts(id) ON DELETE CASCADE;

ALTER TABLE api_key_account_bindings
    DROP CONSTRAINT IF EXISTS api_key_account_bindings_provider_account_id_fkey;

ALTER TABLE api_key_account_bindings
    ADD CONSTRAINT api_key_account_bindings_provider_account_id_fkey
    FOREIGN KEY (provider_account_id) REFERENCES provider_accounts(id) ON DELETE CASCADE;

ALTER TABLE provider_accounts
    ADD COLUMN IF NOT EXISTS reset_credits_refresh_claimed_until timestamptz;

CREATE TABLE IF NOT EXISTS admin_sessions (
    session_hash bytea PRIMARY KEY,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS admin_sessions_expires_at_idx
    ON admin_sessions(expires_at);

CREATE TABLE IF NOT EXISTS admin_login_failures (
    scope_key text PRIMARY KEY,
    failure_count integer NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
    reset_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS admin_login_failures_reset_at_idx
    ON admin_login_failures(reset_at);

CREATE TABLE IF NOT EXISTS api_key_rate_limits (
    api_key_id uuid PRIMARY KEY REFERENCES api_keys(id) ON DELETE CASCADE,
    window_start timestamptz NOT NULL,
    request_count integer NOT NULL DEFAULT 0 CHECK (request_count >= 0)
);
