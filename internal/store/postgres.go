package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool *pgxpool.Pool
}

func Open(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	p := &Postgres{pool: pool}
	if err = p.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if err = p.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

func (p *Postgres) Ping(ctx context.Context) error {
	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	return nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) migrate(ctx context.Context) error {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, readErr := migrations.Files.ReadFile(entry.Name())
		if readErr != nil {
			return fmt.Errorf("read migration %s: %w", entry.Name(), readErr)
		}
		tx, beginErr := p.pool.Begin(ctx)
		if beginErr != nil {
			return fmt.Errorf("begin migration %s: %w", entry.Name(), beginErr)
		}
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(731242001)"); err == nil {
			_, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`)
		}
		if err == nil {
			var applied bool
			err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, entry.Name()).Scan(&applied)
			if err == nil && !applied {
				_, err = tx.Exec(ctx, string(body))
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES ($1) ON CONFLICT DO NOTHING`, entry.Name())
				}
			}
		}
		if err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", entry.Name(), err)
		}
		if err = tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (p *Postgres) CreateProviderAccount(ctx context.Context, a domain.ProviderAccount) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO provider_accounts
		(id, provider, credential_type, display_name, email, subject_hmac, credential_ciphertext, credential_version, status, quota_snapshot,
		 health_status,last_checked_at,last_health_error_code,consecutive_health_failures,next_health_check_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,COALESCE($10,'{}'::jsonb),COALESCE(NULLIF($11,''),'unknown'),$12,NULLIF($13,''),$14,$15)`,
		a.ID, a.Provider, a.CredentialType, a.DisplayName, a.Email, a.SubjectHMAC, a.CredentialCiphertext, a.CredentialVersion, a.Status, nullableJSON(a.QuotaSnapshot),
		a.HealthStatus, a.LastCheckedAt, a.LastHealthErrorCode, a.ConsecutiveFailures, a.NextHealthCheckAt)
	return wrapDB("create provider account", err)
}

func (p *Postgres) ListProviderAccounts(ctx context.Context) ([]domain.ProviderAccount, error) {
	rows, err := p.pool.Query(ctx, `SELECT a.id,a.provider,a.credential_type,a.display_name,COALESCE(a.email,''),a.credential_version,a.status,
		a.fast_mode_enabled,
		(SELECT count(*) FROM api_key_account_bindings b JOIN api_keys k ON k.id=b.api_key_id WHERE b.provider_account_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())),
		a.quota_snapshot,a.cooldown_until,a.last_success_at,a.last_failure_at,a.health_status,a.last_checked_at,COALESCE(a.last_health_error_code,''),a.consecutive_health_failures,a.next_health_check_at,a.created_at,a.updated_at
		FROM provider_accounts a ORDER BY a.created_at`)
	if err != nil {
		return nil, wrapDB("list provider accounts", err)
	}
	defer rows.Close()
	var out []domain.ProviderAccount
	for rows.Next() {
		var a domain.ProviderAccount
		if err = rows.Scan(&a.ID, &a.Provider, &a.CredentialType, &a.DisplayName, &a.Email, &a.CredentialVersion, &a.Status,
			&a.FastModeEnabled, &a.AssignedAPIKeys, &a.QuotaSnapshot, &a.CooldownUntil, &a.LastSuccessAt, &a.LastFailureAt, &a.HealthStatus, &a.LastCheckedAt, &a.LastHealthErrorCode, &a.ConsecutiveFailures, &a.NextHealthCheckAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, wrapDB("scan provider account", err)
		}
		out = append(out, a)
	}
	return out, wrapDB("list provider accounts", rows.Err())
}

func (p *Postgres) ListPoolProviderAccounts(ctx context.Context, poolID string) ([]domain.ProviderAccount, error) {
	rows, err := p.pool.Query(ctx, `SELECT a.id,a.provider,a.credential_type,a.display_name,COALESCE(a.email,''),a.credential_ciphertext,a.credential_version,a.status,
		a.fast_mode_enabled,a.health_status,a.quota_snapshot,a.cooldown_until,a.last_success_at,a.last_failure_at,a.created_at,a.updated_at
		FROM provider_accounts a JOIN pool_accounts pa ON pa.provider_account_id=a.id
		WHERE pa.pool_id=$1 AND pa.enabled
		AND (a.status='active' OR (a.status='cooling_down' AND a.cooldown_until<=now()))
		AND COALESCE(NULLIF(a.health_status,''),'unknown')!='unhealthy'
		ORDER BY pa.priority,a.id`, poolID)
	if err != nil {
		return nil, wrapDB("list pool provider accounts", err)
	}
	defer rows.Close()
	var accounts []domain.ProviderAccount
	for rows.Next() {
		var account domain.ProviderAccount
		if err = rows.Scan(&account.ID, &account.Provider, &account.CredentialType, &account.DisplayName, &account.Email, &account.CredentialCiphertext, &account.CredentialVersion, &account.Status,
			&account.FastModeEnabled, &account.HealthStatus, &account.QuotaSnapshot, &account.CooldownUntil, &account.LastSuccessAt, &account.LastFailureAt, &account.CreatedAt, &account.UpdatedAt); err != nil {
			return nil, wrapDB("scan pool provider account", err)
		}
		accounts = append(accounts, account)
	}
	return accounts, wrapDB("list pool provider accounts", rows.Err())
}

func (p *Postgres) GetProviderAccount(ctx context.Context, id string) (domain.ProviderAccount, error) {
	var a domain.ProviderAccount
	err := p.pool.QueryRow(ctx, `SELECT id,provider,credential_type,display_name,COALESCE(email,''),credential_ciphertext,credential_version,status,
		fast_mode_enabled,quota_snapshot,cooldown_until,last_success_at,last_failure_at,health_status,last_checked_at,COALESCE(last_health_error_code,''),consecutive_health_failures,next_health_check_at,created_at,updated_at FROM provider_accounts WHERE id=$1`, id).
		Scan(&a.ID, &a.Provider, &a.CredentialType, &a.DisplayName, &a.Email, &a.CredentialCiphertext, &a.CredentialVersion, &a.Status,
			&a.FastModeEnabled, &a.QuotaSnapshot, &a.CooldownUntil, &a.LastSuccessAt, &a.LastFailureAt, &a.HealthStatus, &a.LastCheckedAt, &a.LastHealthErrorCode, &a.ConsecutiveFailures, &a.NextHealthCheckAt, &a.CreatedAt, &a.UpdatedAt)
	return a, wrapDB("get provider account", err)
}

func (p *Postgres) UpdateProviderDetails(ctx context.Context, id, email string, quota []byte) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET email=COALESCE(NULLIF($2,''),email),quota_snapshot=COALESCE($3,'{}'::jsonb),updated_at=now() WHERE id=$1`, id, email, nullableJSON(quota))
	return wrapMutation("update provider details", tag.RowsAffected(), err)
}

func (p *Postgres) GetProviderResetCredits(ctx context.Context, id string) ([]byte, *time.Time, error) {
	var snapshot []byte
	var checkedAt *time.Time
	err := p.pool.QueryRow(ctx, `SELECT reset_credits_snapshot,reset_credits_checked_at FROM provider_accounts WHERE id=$1`, id).Scan(&snapshot, &checkedAt)
	return snapshot, checkedAt, wrapDB("get provider reset credits", err)
}

func (p *Postgres) SetProviderResetCredits(ctx context.Context, id string, snapshot []byte, checkedAt time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET reset_credits_snapshot=$2,reset_credits_checked_at=$3,
		reset_credits_refresh_claimed_until=NULL,updated_at=now() WHERE id=$1`, id, snapshot, checkedAt)
	return wrapMutation("set provider reset credits", tag.RowsAffected(), err)
}

func (p *Postgres) ClaimProviderResetCreditRefresh(ctx context.Context, id string, staleBefore, claimedUntil time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET reset_credits_refresh_claimed_until=$3
		WHERE id=$1 AND (reset_credits_checked_at IS NULL OR reset_credits_checked_at<$2)
		AND (reset_credits_refresh_claimed_until IS NULL OR reset_credits_refresh_claimed_until<now())`, id, staleBefore, claimedUntil)
	if err != nil {
		return false, wrapDB("claim provider reset credit refresh", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (p *Postgres) ReleaseProviderResetCreditRefresh(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET reset_credits_refresh_claimed_until=NULL WHERE id=$1`, id)
	return wrapMutation("release provider reset credit refresh", tag.RowsAffected(), err)
}

func (p *Postgres) UpdateProviderAccount(ctx context.Context, id string, update domain.ProviderAccountUpdate) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET
		display_name=COALESCE($2,display_name),status=COALESCE($3,status),fast_mode_enabled=COALESCE($4,fast_mode_enabled),
		cooldown_until=CASE WHEN $3='active' THEN NULL ELSE cooldown_until END,updated_at=now() WHERE id=$1`,
		id, update.DisplayName, update.Status, update.FastModeEnabled)
	return wrapMutation("update provider account", tag.RowsAffected(), err)
}

func (p *Postgres) GetSettings(ctx context.Context) (domain.Settings, error) {
	var settings domain.Settings
	err := p.pool.QueryRow(ctx, `SELECT max_api_keys_per_account,updated_at FROM global_settings WHERE singleton`).Scan(
		&settings.MaxAPIKeysPerAccount, &settings.UpdatedAt)
	return settings, wrapDB("get settings", err)
}

func (p *Postgres) UpdateSettings(ctx context.Context, settings domain.Settings) error {
	tag, err := p.pool.Exec(ctx, `UPDATE global_settings SET max_api_keys_per_account=$1,updated_at=now() WHERE singleton`, settings.MaxAPIKeysPerAccount)
	return wrapMutation("update settings", tag.RowsAffected(), err)
}

func (p *Postgres) DeleteProviderAccount(ctx context.Context, id string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin provider account deletion", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731242002)`); err != nil {
		return wrapDB("lock provider account deletion", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM provider_accounts a WHERE a.id=$1
		AND NOT EXISTS (
			SELECT 1 FROM api_key_account_bindings b JOIN api_keys k ON k.id=b.api_key_id
			WHERE b.provider_account_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())
		)`, id)
	if err != nil {
		return wrapDB("delete provider account", err)
	}
	if tag.RowsAffected() == 0 {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provider_accounts WHERE id=$1)`, id).Scan(&exists); err != nil {
			return wrapDB("check provider account deletion", err)
		}
		if exists {
			return ErrConflict
		}
		return ErrNotFound
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit provider account deletion", err)
	}
	return nil
}

func (p *Postgres) UpdateProviderCredentialCAS(ctx context.Context, id string, expectedVersion int, ciphertext []byte, version int) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET credential_ciphertext=$3,credential_version=$4,status='active',cooldown_until=NULL,updated_at=now() WHERE id=$1 AND credential_version=$2`, id, expectedVersion, ciphertext, version)
	if err != nil {
		return false, wrapDB("update provider credential", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (p *Postgres) UpdateProviderStatus(ctx context.Context, id, status string, cooldown *time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET status=$2,cooldown_until=$3,
		last_failure_at=CASE WHEN $2='active' THEN last_failure_at ELSE now() END,
		health_status=CASE WHEN $2='auth_failed' THEN 'unhealthy' WHEN $2='cooling_down' THEN 'healthy' ELSE health_status END,
		last_checked_at=CASE WHEN $2 IN ('auth_failed','cooling_down') THEN now() ELSE last_checked_at END,
		last_health_error_code=CASE WHEN $2='auth_failed' THEN 'authentication_failed' WHEN $2='cooling_down' THEN NULL ELSE last_health_error_code END,
		consecutive_health_failures=CASE WHEN $2='auth_failed' THEN 3 WHEN $2='cooling_down' THEN 0 ELSE consecutive_health_failures END,
		next_health_check_at=CASE WHEN $2 IN ('auth_failed','cooling_down') THEN now()+interval '5 minutes' ELSE next_health_check_at END,
		updated_at=now() WHERE id=$1`, id, status, cooldown)
	return wrapMutation("update provider status", tag.RowsAffected(), err)
}

func (p *Postgres) SetProviderHealth(ctx context.Context, id, healthStatus, errorCode string, checkedAt, nextCheckAt time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET health_status=$2,last_checked_at=$4,last_health_error_code=NULLIF($3,''),
		consecutive_health_failures=CASE WHEN $2 IN ('healthy','unknown') THEN 0 ELSE consecutive_health_failures END,
		next_health_check_at=$5,updated_at=now() WHERE id=$1`, id, healthStatus, errorCode, checkedAt, nextCheckAt)
	return wrapMutation("set provider health", tag.RowsAffected(), err)
}

func (p *Postgres) RecordProviderHealthFailure(ctx context.Context, id, errorCode string, checkedAt, nextCheckAt time.Time) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET
		consecutive_health_failures=consecutive_health_failures+1,
		health_status=CASE WHEN consecutive_health_failures+1>=3 THEN 'unhealthy' ELSE health_status END,
		last_checked_at=$3,last_health_error_code=$2,next_health_check_at=$4,updated_at=now() WHERE id=$1`, id, errorCode, checkedAt, nextCheckAt)
	return wrapMutation("record provider health failure", tag.RowsAffected(), err)
}

func (p *Postgres) ClaimProviderHealthChecks(ctx context.Context, limit int, now, claimedUntil time.Time) ([]domain.ProviderAccount, error) {
	rows, err := p.pool.Query(ctx, `WITH due AS (
		SELECT id FROM provider_accounts WHERE status='active' AND (next_health_check_at IS NULL OR next_health_check_at<=$1)
		ORDER BY next_health_check_at NULLS FIRST,id FOR UPDATE SKIP LOCKED LIMIT $2
	) UPDATE provider_accounts a SET next_health_check_at=$3,updated_at=now() FROM due WHERE a.id=due.id
	RETURNING a.id,a.provider,a.credential_type,a.display_name,a.credential_ciphertext,a.credential_version,a.status,
		a.health_status,a.last_checked_at,COALESCE(a.last_health_error_code,''),a.consecutive_health_failures,a.next_health_check_at`, now, limit, claimedUntil)
	if err != nil {
		return nil, wrapDB("claim provider health checks", err)
	}
	defer rows.Close()
	var accounts []domain.ProviderAccount
	for rows.Next() {
		var account domain.ProviderAccount
		if err = rows.Scan(&account.ID, &account.Provider, &account.CredentialType, &account.DisplayName, &account.CredentialCiphertext, &account.CredentialVersion, &account.Status,
			&account.HealthStatus, &account.LastCheckedAt, &account.LastHealthErrorCode, &account.ConsecutiveFailures, &account.NextHealthCheckAt); err != nil {
			return nil, wrapDB("scan provider health check", err)
		}
		accounts = append(accounts, account)
	}
	return accounts, wrapDB("claim provider health checks", rows.Err())
}
