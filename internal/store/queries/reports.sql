-- name: ReportTrackingStarted :one
SELECT started_at FROM report_tracking WHERE singleton;

-- name: ReportAccounts :many
SELECT a.id, a.display_name, COALESCE(a.email, '')::text AS email, a.quota_snapshot, a.quota_checked_at,
    count(e.response_hash)::bigint AS requests
FROM provider_accounts a
LEFT JOIN provider_request_events e ON e.provider_account_id = a.id
    AND e.occurred_at >= sqlc.arg(window_start) AND e.occurred_at < sqlc.arg(window_end)
WHERE a.provider = 'codex'
GROUP BY a.id ORDER BY a.display_name;

-- name: ClaimDailyReport :one
INSERT INTO daily_team_reports(report_day, payload, lease_until)
VALUES(sqlc.arg(report_day), sqlc.arg(payload), now() + interval '5 minutes')
ON CONFLICT(report_day) DO UPDATE SET
    lease_until = now() + interval '5 minutes', attempts = daily_team_reports.attempts + 1
WHERE daily_team_reports.email_id IS NULL AND daily_team_reports.lease_until < now()
    AND daily_team_reports.attempts < 5
RETURNING payload;

-- name: FinishDailyReport :exec
UPDATE daily_team_reports SET email_id = sqlc.arg(email_id), sent_at = now()
WHERE report_day = sqlc.arg(report_day) AND email_id IS NULL;

-- name: RetryDailyReport :exec
UPDATE daily_team_reports SET lease_until = now() + interval '1 minute'
WHERE report_day = sqlc.arg(report_day) AND email_id IS NULL;
