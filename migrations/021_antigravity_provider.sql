ALTER TABLE provider_accounts DROP CONSTRAINT IF EXISTS provider_accounts_provider_check;
ALTER TABLE provider_accounts ADD CONSTRAINT provider_accounts_provider_check
    CHECK (provider IN ('codex', 'copilot', 'openai_compatible', 'antigravity'));

ALTER TABLE pools DROP CONSTRAINT IF EXISTS pools_provider_check;
ALTER TABLE pools ADD CONSTRAINT pools_provider_check
    CHECK (provider IN ('codex', 'copilot', 'openai_compatible', 'antigravity', 'mixed'));

CREATE TABLE antigravity_tool_signatures (
    provider_account_id uuid NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
    model text NOT NULL,
    call_id text NOT NULL,
    ciphertext bytea NOT NULL CHECK (octet_length(ciphertext) <= 262144),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (provider_account_id, model, call_id)
);
CREATE INDEX antigravity_tool_signatures_expiration_idx ON antigravity_tool_signatures(expires_at);
