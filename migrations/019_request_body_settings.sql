-- Zero keeps the existing environment defaults until request limits are saved.
ALTER TABLE global_settings
    ADD COLUMN max_request_body_bytes bigint NOT NULL DEFAULT 0,
    ADD COLUMN max_inflight_request_body_bytes bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT global_settings_request_body_limits CHECK (
        (max_request_body_bytes = 0 AND max_inflight_request_body_bytes = 0)
        OR (max_request_body_bytes > 0
            AND max_inflight_request_body_bytes / 4 >= max_request_body_bytes)
    );
