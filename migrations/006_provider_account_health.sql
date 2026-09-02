ALTER TABLE provider_accounts
    ADD COLUMN IF NOT EXISTS health_status text NOT NULL DEFAULT 'unknown'
        CHECK (health_status IN ('unknown', 'healthy', 'unhealthy')),
    ADD COLUMN IF NOT EXISTS last_checked_at timestamptz,
    ADD COLUMN IF NOT EXISTS last_health_error_code text,
    ADD COLUMN IF NOT EXISTS consecutive_health_failures integer NOT NULL DEFAULT 0
        CHECK (consecutive_health_failures >= 0),
    ADD COLUMN IF NOT EXISTS next_health_check_at timestamptz;

CREATE INDEX IF NOT EXISTS provider_accounts_health_due_idx
    ON provider_accounts(next_health_check_at)
    WHERE status = 'active';
