package store

import (
	"context"
	"errors"
	"time"

	"github.com/gesta-run/subpool/internal/reporting"
	"github.com/gesta-run/subpool/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (p *Postgres) ReportTrackingStarted(ctx context.Context) (time.Time, error) {
	value, err := p.queries.ReportTrackingStarted(ctx)
	return value.Time, wrapDB("read report tracking start", err)
}
func (p *Postgres) ReportAccounts(ctx context.Context, start, end time.Time) ([]reporting.Account, error) {
	rows, err := p.queries.ReportAccounts(ctx, storedb.ReportAccountsParams{WindowStart: dbTime(start), WindowEnd: dbTime(end)})
	if err != nil {
		return nil, wrapDB("read report accounts", err)
	}
	accounts := make([]reporting.Account, 0, len(rows))
	for _, row := range rows {
		account := reporting.Account{ID: row.ID, Name: row.DisplayName, Requests: row.Requests, Snapshot: row.QuotaSnapshot}
		if row.QuotaCheckedAt.Valid {
			account.CheckedAt = row.QuotaCheckedAt.Time
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}
func reportDay(day string) (pgtype.Date, error) {
	parsed, err := time.Parse("2006-01-02", day)
	return pgtype.Date{Time: parsed, Valid: err == nil}, err
}
func (p *Postgres) ClaimDailyReport(ctx context.Context, day string, payload []byte) ([]byte, bool, error) {
	date, err := reportDay(day)
	if err != nil {
		return nil, false, err
	}
	frozen, err := p.queries.ClaimDailyReport(ctx, storedb.ClaimDailyReportParams{ReportDay: date, Payload: payload})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return frozen, err == nil, wrapDB("claim daily report", err)
}
func (p *Postgres) FinishDailyReport(ctx context.Context, day, emailID string) error {
	date, err := reportDay(day)
	if err != nil {
		return err
	}
	return wrapDB("finish daily report", p.queries.FinishDailyReport(ctx, storedb.FinishDailyReportParams{ReportDay: date, EmailID: &emailID}))
}
func (p *Postgres) RetryDailyReport(ctx context.Context, day string) error {
	date, err := reportDay(day)
	if err != nil {
		return err
	}
	return wrapDB("retry daily report", p.queries.RetryDailyReport(ctx, date))
}
