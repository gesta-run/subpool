CREATE TABLE codex_reset_batches (
    id uuid PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 1,
    payload jsonb,
    email_id text,
    sent_at timestamptz
);

CREATE TABLE codex_reset_jobs (
    account_id uuid NOT NULL REFERENCES provider_accounts(id) ON DELETE CASCADE,
    credit_id text NOT NULL,
    account_label text NOT NULL,
    run_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    batch_id uuid REFERENCES codex_reset_batches(id),
    attempted_at timestamptz,
    outcome text,
    detail text NOT NULL DEFAULT '',
    before_quota jsonb,
    after_quota jsonb,
    finished_at timestamptz,
    PRIMARY KEY(account_id, credit_id)
);
CREATE INDEX codex_reset_jobs_due_idx ON codex_reset_jobs(run_at) WHERE batch_id IS NULL AND outcome IS NULL;
