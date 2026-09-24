package store

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/gesta-run/subpool/internal/store/storedb"
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
	queries := p.queries.WithTx(tx)
	for _, key := range keys {
		if err = queries.LockAdminLoginScope(ctx, key); err != nil {
			return false, wrapDB("lock admin login scope", err)
		}
		var failures storedb.GetAdminLoginFailuresRow
		failures, err = queries.GetAdminLoginFailures(ctx, key)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, wrapDB("read admin login failures", err)
		}
		if err == nil && now.Before(failures.ResetAt.Time) && failures.FailureCount >= adminFailureLimit {
			return false, nil
		}
	}
	if validCredentials {
		if err = queries.ClearAdminLoginFailures(ctx, keys); err != nil {
			return false, wrapDB("clear admin login failures", err)
		}
		if err = tx.Commit(ctx); err != nil {
			return false, wrapDB("commit admin login attempt", err)
		}
		return true, nil
	}
	for _, key := range keys {
		if err = queries.RecordAdminLoginFailure(ctx, storedb.RecordAdminLoginFailureParams{ScopeKey: key, Now: dbTime(now)}); err != nil {
			return false, wrapDB("record admin login failure", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return false, wrapDB("commit admin login failure", err)
	}
	return false, nil
}

func (p *Postgres) CreateAdminSession(ctx context.Context, digest []byte, expiresAt time.Time) error {
	err := p.queries.CreateAdminSession(ctx, storedb.CreateAdminSessionParams{SessionHash: digest, ExpiresAt: dbTime(expiresAt)})
	return wrapDB("create admin session", err)
}

func (p *Postgres) AdminSessionActive(ctx context.Context, digest []byte, now time.Time) (bool, error) {
	active, err := p.queries.AdminSessionActive(ctx, storedb.AdminSessionActiveParams{SessionHash: digest, Now: dbTime(now)})
	return active, wrapDB("validate admin session", err)
}

func (p *Postgres) RevokeAdminSession(ctx context.Context, digest []byte, revokedAt time.Time) error {
	err := p.queries.RevokeAdminSession(ctx, storedb.RevokeAdminSessionParams{RevokedAt: dbTime(revokedAt), SessionHash: digest})
	return wrapDB("revoke admin session", err)
}

func (p *Postgres) AllowAPIKeyRequest(ctx context.Context, keyID string, limit int, now time.Time) (bool, error) {
	if limit <= 0 {
		return true, nil
	}
	allowed, err := p.queries.AllowAPIKeyRequest(ctx, storedb.AllowAPIKeyRequestParams{
		ApiKeyID: keyID, Now: dbTime(now), RequestLimit: int32(limit),
	})
	return allowed, wrapDB("apply API key rate limit", err)
}
