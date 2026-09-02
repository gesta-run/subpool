package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

const adminFailureLimit = 5

func (p *Postgres) RecordAdminLoginAttempt(ctx context.Context, scopeKeys []string, validCredentials bool, now time.Time) (bool, error) {
	keys := append([]string(nil), scopeKeys...)
	sort.Strings(keys)
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return false, wrapDB("begin admin login attempt", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, key := range keys {
		if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,731242003))`, key); err != nil {
			return false, wrapDB("lock admin login scope", err)
		}
		var count int
		var resetAt time.Time
		err = tx.QueryRow(ctx, `SELECT failure_count,reset_at FROM admin_login_failures WHERE scope_key=$1`, key).Scan(&count, &resetAt)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, wrapDB("read admin login failures", err)
		}
		if err == nil && now.Before(resetAt) && count >= adminFailureLimit {
			return false, nil
		}
	}
	if validCredentials {
		if _, err = tx.Exec(ctx, `DELETE FROM admin_login_failures WHERE scope_key=ANY($1::text[])`, keys); err != nil {
			return false, wrapDB("clear admin login failures", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return false, wrapDB("commit admin login attempt", err)
		}
		return true, nil
	}
	for _, key := range keys {
		if _, err = tx.Exec(ctx, `INSERT INTO admin_login_failures(scope_key,failure_count,reset_at)
			VALUES($1,1,$2::timestamptz+interval '1 minute')
			ON CONFLICT(scope_key) DO UPDATE SET
				failure_count=CASE WHEN admin_login_failures.reset_at<=$2::timestamptz THEN 1 ELSE admin_login_failures.failure_count+1 END,
				reset_at=CASE WHEN admin_login_failures.reset_at<=$2::timestamptz THEN $2::timestamptz+interval '1 minute' ELSE admin_login_failures.reset_at END,
				updated_at=now()`, key, now); err != nil {
			return false, wrapDB("record admin login failure", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, wrapDB("commit admin login failure", err)
	}
	return false, nil
}

func (p *Postgres) CreateAdminSession(ctx context.Context, digest []byte, expiresAt time.Time) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO admin_sessions(session_hash,expires_at) VALUES($1,$2)
		ON CONFLICT(session_hash) DO UPDATE SET expires_at=excluded.expires_at,revoked_at=NULL`, digest, expiresAt)
	return wrapDB("create admin session", err)
}

func (p *Postgres) AdminSessionActive(ctx context.Context, digest []byte, now time.Time) (bool, error) {
	var active bool
	err := p.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_sessions
		WHERE session_hash=$1 AND revoked_at IS NULL AND expires_at>$2)`, digest, now).Scan(&active)
	return active, wrapDB("validate admin session", err)
}

func (p *Postgres) RevokeAdminSession(ctx context.Context, digest []byte, revokedAt time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE admin_sessions SET revoked_at=$2 WHERE session_hash=$1 AND revoked_at IS NULL`, digest, revokedAt)
	return wrapDB("revoke admin session", err)
}

func (p *Postgres) AllowAPIKeyRequest(ctx context.Context, keyID string, limit int, now time.Time) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	var allowed bool
	err := p.pool.QueryRow(ctx, `INSERT INTO api_key_rate_limits(api_key_id,window_start,request_count)
		VALUES($1,date_trunc('minute',$2::timestamptz),1)
		ON CONFLICT(api_key_id) DO UPDATE SET
			window_start=CASE WHEN api_key_rate_limits.window_start<date_trunc('minute',$2::timestamptz)
				THEN date_trunc('minute',$2::timestamptz) ELSE api_key_rate_limits.window_start END,
			request_count=CASE WHEN api_key_rate_limits.window_start<date_trunc('minute',$2::timestamptz)
				THEN 1 ELSE api_key_rate_limits.request_count+1 END
		RETURNING request_count<=$3`, keyID, now, limit).Scan(&allowed)
	return allowed, wrapDB("apply API key rate limit", err)
}
