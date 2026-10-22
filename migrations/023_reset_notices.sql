CREATE TABLE codex_reset_notices (
    notice_date date NOT NULL,
    channel_id text NOT NULL,
    message text NOT NULL,
    outcome text NOT NULL DEFAULT 'attempting',
    message_link text,
    attempted_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    PRIMARY KEY (notice_date, channel_id)
);
