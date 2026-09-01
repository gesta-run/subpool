CREATE TABLE IF NOT EXISTS usage_event_dedup (
    api_key_id uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    event_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (api_key_id, event_hash)
);

CREATE INDEX IF NOT EXISTS usage_event_dedup_created_at_idx ON usage_event_dedup(created_at);
