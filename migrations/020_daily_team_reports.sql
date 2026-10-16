CREATE TABLE provider_request_events (
    api_key_id uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    response_hash bytea NOT NULL,
    provider_account_id uuid NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
    occurred_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (api_key_id, response_hash)
);
CREATE INDEX provider_request_events_time_idx ON provider_request_events(occurred_at, provider_account_id);

CREATE TABLE report_tracking (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    started_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO report_tracking(singleton) VALUES(true);

CREATE TABLE daily_team_reports (
    report_day date PRIMARY KEY,
    payload jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 1,
    lease_until timestamptz NOT NULL,
    email_id text,
    sent_at timestamptz
);
