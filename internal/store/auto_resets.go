package store

import (
	"context"
	"errors"
	"time"

	"github.com/gesta-run/subpool/internal/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type ResetJob struct {
	AccountID, CreditID, Label, Outcome, Detail string
	RunAt, ExpiresAt                            time.Time
	AttemptedAt                                 pgtype.Timestamptz
	Before, After                               []byte
}

type ResetBatch struct {
	ID      string
	Payload []byte
	Jobs    []ResetJob
}

func (p *Postgres) PlanReset(ctx context.Context, j ResetJob) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO codex_reset_jobs(account_id,credit_id,account_label,run_at,expires_at)
        VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id,credit_id) DO UPDATE SET
        account_label=EXCLUDED.account_label,run_at=EXCLUDED.run_at,expires_at=EXCLUDED.expires_at
        WHERE codex_reset_jobs.batch_id IS NULL AND codex_reset_jobs.outcome IS NULL`, j.AccountID, j.CreditID, j.Label, j.RunAt, j.ExpiresAt)
	return err
}

func (p *Postgres) ClaimResetBatch(ctx context.Context, now time.Time, allowNew bool) (*ResetBatch, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	b := &ResetBatch{}
	err = tx.QueryRow(ctx, `SELECT id,payload FROM codex_reset_batches
        WHERE email_id IS NULL AND lease_until<now() AND attempts<5
        ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&b.ID, &b.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		if !allowNew {
			return nil, nil
		}
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM codex_reset_jobs WHERE batch_id IS NULL AND outcome IS NULL AND run_at<=$1`, now).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			return nil, nil
		}
		if b.ID, err = id.New(); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO codex_reset_batches(id,lease_until) VALUES($1,now()+interval '10 minutes')`, b.ID); err != nil {
			return nil, err
		}
		tag, updateErr := tx.Exec(ctx, `UPDATE codex_reset_jobs SET batch_id=$1 WHERE (account_id,credit_id) IN
            (SELECT account_id,credit_id FROM codex_reset_jobs WHERE batch_id IS NULL AND outcome IS NULL AND run_at<=$2
            ORDER BY run_at,expires_at,account_id FOR UPDATE SKIP LOCKED LIMIT 20)`, b.ID, now)
		if updateErr != nil {
			return nil, updateErr
		}
		if tag.RowsAffected() == 0 {
			return nil, nil
		}
	} else if err != nil {
		return nil, err
	} else {
		if _, err = tx.Exec(ctx, `UPDATE codex_reset_batches SET lease_until=now()+interval '10 minutes',attempts=attempts+1 WHERE id=$1`, b.ID); err != nil {
			return nil, err
		}
	}
	rows, err := tx.Query(ctx, `SELECT account_id,credit_id,account_label,run_at,expires_at,attempted_at,COALESCE(outcome,''),detail,before_quota,after_quota
        FROM codex_reset_jobs WHERE batch_id=$1 ORDER BY expires_at,account_id`, b.ID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var j ResetJob
		if err = rows.Scan(&j.AccountID, &j.CreditID, &j.Label, &j.RunAt, &j.ExpiresAt, &j.AttemptedAt, &j.Outcome, &j.Detail, &j.Before, &j.After); err != nil {
			rows.Close()
			return nil, err
		}
		b.Jobs = append(b.Jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

func (p *Postgres) StartResetAttempt(ctx context.Context, j ResetJob) error {
	_, err := p.pool.Exec(ctx, `UPDATE codex_reset_jobs SET attempted_at=now(),before_quota=$3 WHERE account_id=$1 AND credit_id=$2 AND attempted_at IS NULL`, j.AccountID, j.CreditID, j.Before)
	return err
}

func (p *Postgres) FinishResetJob(ctx context.Context, j ResetJob) error {
	_, err := p.pool.Exec(ctx, `UPDATE codex_reset_jobs SET outcome=$3,detail=$4,before_quota=COALESCE(before_quota,$5),after_quota=$6,finished_at=now()
        WHERE account_id=$1 AND credit_id=$2 AND outcome IS NULL`, j.AccountID, j.CreditID, j.Outcome, j.Detail, j.Before, j.After)
	return err
}

func (p *Postgres) FreezeResetReport(ctx context.Context, batchID string, payload []byte) ([]byte, error) {
	var frozen []byte
	err := p.pool.QueryRow(ctx, `UPDATE codex_reset_batches SET payload=COALESCE(payload,$2) WHERE id=$1 RETURNING payload`, batchID, payload).Scan(&frozen)
	return frozen, err
}

func (p *Postgres) FinishResetReport(ctx context.Context, batchID, emailID string) error {
	_, err := p.pool.Exec(ctx, `UPDATE codex_reset_batches SET email_id=$2,sent_at=now() WHERE id=$1`, batchID, emailID)
	return err
}

func (p *Postgres) RetryResetBatch(ctx context.Context, batchID string) error {
	_, err := p.pool.Exec(ctx, `UPDATE codex_reset_batches SET lease_until=now()+interval '1 minute' WHERE id=$1 AND email_id IS NULL`, batchID)
	return err
}
