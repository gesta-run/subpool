CREATE TABLE IF NOT EXISTS schema_migrations (
    version text PRIMARY KEY,
    applied_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS provider_accounts (
    id uuid PRIMARY KEY,
    provider text NOT NULL CHECK (provider = 'codex'),
    credential_type text NOT NULL CHECK (credential_type = 'subscription_oauth'),
    display_name text NOT NULL,
    subject_hmac bytea NOT NULL,
    credential_ciphertext bytea NOT NULL,
    credential_version integer NOT NULL DEFAULT 1,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'cooling_down', 'exhausted', 'auth_failed', 'disabled')),
    max_api_keys integer NOT NULL DEFAULT 3 CHECK (max_api_keys > 0),
    quota_snapshot jsonb NOT NULL DEFAULT '{}'::jsonb,
    cooldown_until timestamptz,
    last_success_at timestamptz,
    last_failure_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS provider_accounts_subject_idx ON provider_accounts(provider, subject_hmac);

CREATE TABLE IF NOT EXISTS pools (
    id uuid PRIMARY KEY,
    name text NOT NULL UNIQUE,
    provider text NOT NULL CHECK (provider = 'codex'),
    strategy text NOT NULL DEFAULT 'least_assigned' CHECK (strategy = 'least_assigned'),
    model_allowlist text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS pool_accounts (
    pool_id uuid NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
    provider_account_id uuid NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
    weight integer NOT NULL DEFAULT 1 CHECK (weight > 0),
    priority integer NOT NULL DEFAULT 0,
    enabled boolean NOT NULL DEFAULT true,
    PRIMARY KEY (pool_id, provider_account_id)
);

CREATE TABLE IF NOT EXISTS api_keys (
    id uuid PRIMARY KEY,
    pool_id uuid NOT NULL REFERENCES pools(id) ON DELETE RESTRICT,
    employee_name text NOT NULL,
    key_hmac bytea NOT NULL UNIQUE,
    key_hint text NOT NULL,
    scopes text[] NOT NULL DEFAULT '{}',
    rate_limit integer NOT NULL DEFAULT 0 CHECK (rate_limit >= 0),
    expires_at timestamptz,
    revoked_at timestamptz,
    last_used_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS api_key_account_bindings (
    api_key_id uuid PRIMARY KEY REFERENCES api_keys(id) ON DELETE CASCADE,
    provider_account_id uuid NOT NULL REFERENCES provider_accounts(id) ON DELETE RESTRICT,
    assigned_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS api_key_account_bindings_account_idx ON api_key_account_bindings(provider_account_id);

CREATE TABLE IF NOT EXISTS session_bindings (
    api_key_id uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    pool_id uuid NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
    session_hash bytea NOT NULL,
    provider_account_id uuid NOT NULL REFERENCES provider_accounts(id) ON DELETE RESTRICT,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (api_key_id, session_hash)
);

CREATE TABLE IF NOT EXISTS api_key_usage_daily (
    api_key_id uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    usage_date date NOT NULL,
    input_tokens bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (api_key_id, usage_date)
);
CREATE INDEX IF NOT EXISTS api_key_usage_daily_date_idx ON api_key_usage_daily(usage_date);

CREATE TABLE IF NOT EXISTS audit_events (
    id bigserial PRIMARY KEY,
    actor text NOT NULL,
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text NOT NULL,
    result text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
