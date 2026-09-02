CREATE INDEX IF NOT EXISTS usage_event_dedup_created_at_idx
    ON usage_event_dedup(created_at);

CREATE INDEX IF NOT EXISTS session_bindings_expires_at_idx
    ON session_bindings(expires_at);
