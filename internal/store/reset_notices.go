package store

import (
	"context"
	"time"

	"github.com/gesta-run/subpool/internal/resetnotice"
)

func (p *Postgres) ResetNoticeEvents(ctx context.Context, start, end time.Time) ([]resetnotice.Event, error) {
	rows, err := p.pool.Query(ctx, `
SELECT account, kind, reset_at FROM (
    SELECT COALESCE(NULLIF(email,''),display_name) AS account, 'weekly reset' AS kind,
        to_timestamp((quota_snapshot->'weekly'->>'reset_at')::bigint) AS reset_at
    FROM provider_accounts WHERE provider='codex' AND credential_type<>'api_key' AND status<>'disabled'
        AND quota_checked_at >= $1::timestamptz - interval '15 minutes'
    UNION ALL
    SELECT COALESCE(NULLIF(a.email,''),a.display_name), 'scheduled reset credit', j.run_at
    FROM codex_reset_jobs j JOIN provider_accounts a ON a.id=j.account_id
    WHERE a.status<>'disabled' AND j.outcome IS NULL AND j.attempted_at IS NULL AND j.expires_at>$1
) events WHERE reset_at >= $1 AND reset_at < $2
ORDER BY reset_at, account, kind`, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []resetnotice.Event
	for rows.Next() {
		var e resetnotice.Event
		if err = rows.Scan(&e.Account, &e.Kind, &e.At); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (p *Postgres) ClaimResetNotice(ctx context.Context, day, channel, message string) (bool, error) {
	date, err := reportDay(day)
	if err != nil {
		return false, err
	}
	tag, err := p.pool.Exec(ctx, `INSERT INTO codex_reset_notices(notice_date,channel_id,message)
VALUES($1::date,$2,$3) ON CONFLICT DO NOTHING`, date, channel, message)
	return tag.RowsAffected() == 1, err
}

func (p *Postgres) FinishResetNotice(ctx context.Context, day, channel, outcome, link string) error {
	date, err := reportDay(day)
	if err != nil {
		return err
	}
	_, err = p.pool.Exec(ctx, `UPDATE codex_reset_notices SET outcome=$3,message_link=NULLIF($4,''),finished_at=now()
WHERE notice_date=$1::date AND channel_id=$2`, date, channel, outcome, link)
	return err
}
