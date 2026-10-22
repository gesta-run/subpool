-- name: ReportTrackingStarted :one
SELECT started_at FROM report_tracking WHERE singleton;

-- name: ReportAccounts :many
WITH employee_counts AS (
    SELECT e.provider_account_id, k.employee_id,
        COALESCE(NULLIF(em.name, ''), 'Unknown')::text AS employee_name,
        count(*)::bigint AS requests
    FROM provider_request_events e
    LEFT JOIN api_keys k ON k.id = e.api_key_id
    LEFT JOIN employees em ON em.id = k.employee_id
    WHERE e.occurred_at >= sqlc.arg(window_start) AND e.occurred_at < sqlc.arg(window_end)
    GROUP BY e.provider_account_id, k.employee_id, em.name
), account_counts AS (
    SELECT provider_account_id, sum(requests)::bigint AS requests,
        jsonb_agg(jsonb_build_object('name', employee_name, 'requests', requests)
            ORDER BY requests DESC, employee_name, employee_id) AS employees
    FROM employee_counts GROUP BY provider_account_id
)
SELECT a.id, a.display_name, COALESCE(a.email, '')::text AS email, a.provider, a.credential_type,
    a.quota_snapshot, a.quota_checked_at, a.reset_credits_snapshot, a.reset_credits_checked_at,
    COALESCE(c.requests, 0)::bigint AS requests,
    COALESCE(c.employees, '[]'::jsonb)::jsonb AS employees
FROM provider_accounts a
LEFT JOIN account_counts c ON c.provider_account_id = a.id
ORDER BY a.created_at;

-- name: RecordProviderRequest :exec
INSERT INTO provider_request_events(api_key_id, response_hash, provider_account_id, occurred_at)
VALUES(sqlc.arg(api_key_id), sqlc.arg(response_hash), sqlc.arg(provider_account_id), sqlc.arg(occurred_at))
ON CONFLICT(api_key_id, response_hash) DO NOTHING;

-- name: ReportUnattributedRequests :one
WITH usage_counts AS (
    SELECT api_key_id, count(*) AS calls FROM usage_event_dedup
    WHERE created_at >= sqlc.arg(window_start) AND created_at < sqlc.arg(window_end)
    GROUP BY api_key_id
), attributed_counts AS (
    SELECT api_key_id, count(*) AS calls FROM provider_request_events
    WHERE occurred_at >= sqlc.arg(window_start) AND occurred_at < sqlc.arg(window_end)
    GROUP BY api_key_id
)
SELECT COALESCE(sum(GREATEST(u.calls - COALESCE(a.calls, 0), 0)), 0)::bigint AS calls
FROM usage_counts u LEFT JOIN attributed_counts a USING(api_key_id);

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
