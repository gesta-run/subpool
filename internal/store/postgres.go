package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/store/storedb"
	"github.com/gesta-run/subpool/migrations"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct {
	pool    *pgxpool.Pool
	queries *storedb.Queries
}

func Open(ctx context.Context, databaseURL string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	p := &Postgres{pool: pool, queries: storedb.New(pool)}
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
		queries := p.queries.WithTx(tx)
		err = queries.LockMigrations(ctx)
		if err == nil {
			err = queries.EnsureSchemaMigrations(ctx)
		}
		if err == nil {
			var applied bool
			applied, err = queries.MigrationApplied(ctx, entry.Name())
			if err == nil && !applied {
				_, err = tx.Exec(ctx, string(body))
				if err == nil {
					err = queries.RecordMigration(ctx, entry.Name())
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

func (p *Postgres) CreateProviderAccount(ctx context.Context, account domain.ProviderAccount) error {
	quota := account.QuotaSnapshot
	if len(quota) == 0 {
		quota = []byte(`{}`)
	}
	err := p.queries.CreateProviderAccount(ctx, storedb.CreateProviderAccountParams{
		ID: account.ID, Provider: account.Provider, CredentialType: account.CredentialType,
		DisplayName: account.DisplayName, Email: account.Email, SubjectHmac: account.SubjectHMAC,
		CredentialCiphertext: account.CredentialCiphertext, CredentialVersion: int32(account.CredentialVersion),
		Status: account.Status, QuotaSnapshot: quota, QuotaCheckedAt: optionalDBTime(account.QuotaCheckedAt),
		LastQuotaErrorCode: account.LastQuotaErrorCode, HealthStatus: account.HealthStatus,
		LastCheckedAt: optionalDBTime(account.LastCheckedAt), LastHealthErrorCode: account.LastHealthErrorCode,
		ConsecutiveHealthFailures: int32(account.ConsecutiveFailures), NextHealthCheckAt: optionalDBTime(account.NextHealthCheckAt),
	})
	return wrapDB("create provider account", err)
}

func (p *Postgres) ListProviderAccounts(ctx context.Context) ([]domain.ProviderAccount, error) {
	rows, err := p.queries.ListProviderAccounts(ctx)
	if err != nil {
		return nil, wrapDB("list provider accounts", err)
	}
	var accounts []domain.ProviderAccount
	for _, row := range rows {
		accounts = append(accounts, domain.ProviderAccount{
			ID: row.ID, Provider: row.Provider, CredentialType: row.CredentialType,
			DisplayName: row.DisplayName, Email: row.Email, CredentialVersion: int(row.CredentialVersion),
			Status: row.Status, FastModeEnabled: row.FastModeEnabled, AssignedAPIKeys: int(row.AssignedApiKeys),
			QuotaSnapshot: row.QuotaSnapshot, QuotaCheckedAt: optionalTime(row.QuotaCheckedAt),
			LastQuotaErrorCode: row.LastQuotaErrorCode, CooldownUntil: optionalTime(row.CooldownUntil),
			LastSuccessAt: optionalTime(row.LastSuccessAt), LastFailureAt: optionalTime(row.LastFailureAt),
			HealthStatus: row.HealthStatus, LastCheckedAt: optionalTime(row.LastCheckedAt),
			LastHealthErrorCode: row.LastHealthErrorCode, ConsecutiveFailures: int(row.ConsecutiveHealthFailures),
			NextHealthCheckAt: optionalTime(row.NextHealthCheckAt), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return accounts, nil
}

func (p *Postgres) ListPoolProviderAccounts(ctx context.Context, poolID string) ([]domain.ProviderAccount, error) {
	rows, err := p.queries.ListPoolProviderAccounts(ctx, poolID)
	if err != nil {
		return nil, wrapDB("list pool provider accounts", err)
	}
	var accounts []domain.ProviderAccount
	for _, row := range rows {
		accounts = append(accounts, domain.ProviderAccount{
			ID: row.ID, Provider: row.Provider, CredentialType: row.CredentialType,
			DisplayName: row.DisplayName, Email: row.Email, CredentialCiphertext: row.CredentialCiphertext,
			CredentialVersion: int(row.CredentialVersion), Status: row.Status, FastModeEnabled: row.FastModeEnabled,
			HealthStatus: row.HealthStatus, QuotaSnapshot: row.QuotaSnapshot,
			CooldownUntil: optionalTime(row.CooldownUntil), LastSuccessAt: optionalTime(row.LastSuccessAt),
			LastFailureAt: optionalTime(row.LastFailureAt), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		})
	}
	return accounts, nil
}

func (p *Postgres) GetProviderAccount(ctx context.Context, id string) (domain.ProviderAccount, error) {
	row, err := p.queries.GetProviderAccount(ctx, id)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("get provider account", err)
	}
	return domain.ProviderAccount{
		ID: row.ID, Provider: row.Provider, CredentialType: row.CredentialType,
		DisplayName: row.DisplayName, Email: row.Email, CredentialCiphertext: row.CredentialCiphertext,
		CredentialVersion: int(row.CredentialVersion), Status: row.Status, FastModeEnabled: row.FastModeEnabled,
		QuotaSnapshot: row.QuotaSnapshot, QuotaCheckedAt: optionalTime(row.QuotaCheckedAt),
		LastQuotaErrorCode: row.LastQuotaErrorCode, CooldownUntil: optionalTime(row.CooldownUntil),
		LastSuccessAt: optionalTime(row.LastSuccessAt), LastFailureAt: optionalTime(row.LastFailureAt),
		HealthStatus: row.HealthStatus, LastCheckedAt: optionalTime(row.LastCheckedAt),
		LastHealthErrorCode: row.LastHealthErrorCode, ConsecutiveFailures: int(row.ConsecutiveHealthFailures),
		NextHealthCheckAt: optionalTime(row.NextHealthCheckAt), CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}, nil
}

func (p *Postgres) UpdateProviderDetails(ctx context.Context, id, email string, quota []byte, quotaCheckedAt time.Time) error {
	var affected int64
	var err error
	if len(quota) == 0 {
		affected, err = p.queries.UpdateProviderEmail(ctx, storedb.UpdateProviderEmailParams{Email: email, ID: id})
	} else {
		affected, err = p.queries.UpdateProviderDetailsWithQuota(ctx, storedb.UpdateProviderDetailsWithQuotaParams{
			Email: email, QuotaSnapshot: quota, QuotaCheckedAt: dbTime(quotaCheckedAt), ID: id,
		})
	}
	return wrapMutation("update provider details", affected, err)
}

func (p *Postgres) SetProviderQuotaError(ctx context.Context, id, errorCode string, nextCheckAt time.Time) error {
	affected, err := p.queries.SetProviderQuotaError(ctx, storedb.SetProviderQuotaErrorParams{
		ErrorCode: errorCode, NextCheckAt: dbTime(nextCheckAt), ID: id,
	})
	return wrapMutation("set provider quota error", affected, err)
}

func (p *Postgres) GetProviderResetCredits(ctx context.Context, id string) ([]byte, *time.Time, error) {
	row, err := p.queries.GetProviderResetCredits(ctx, id)
	return row.ResetCreditsSnapshot, optionalTime(row.ResetCreditsCheckedAt), wrapDB("get provider reset credits", err)
}

func (p *Postgres) SetProviderResetCredits(ctx context.Context, id string, snapshot []byte, checkedAt time.Time) error {
	affected, err := p.queries.SetProviderResetCredits(ctx, storedb.SetProviderResetCreditsParams{
		Snapshot: snapshot, CheckedAt: dbTime(checkedAt), ID: id,
	})
	return wrapMutation("set provider reset credits", affected, err)
}

func (p *Postgres) ClaimProviderResetCreditRefresh(ctx context.Context, id string, staleBefore, claimedUntil time.Time) (bool, error) {
	affected, err := p.queries.ClaimProviderResetCreditRefresh(ctx, storedb.ClaimProviderResetCreditRefreshParams{
		ClaimedUntil: dbTime(claimedUntil), ID: id, StaleBefore: dbTime(staleBefore),
	})
	if err != nil {
		return false, wrapDB("claim provider reset credit refresh", err)
	}
	return affected == 1, nil
}

func (p *Postgres) ReleaseProviderResetCreditRefresh(ctx context.Context, id string) error {
	affected, err := p.queries.ReleaseProviderResetCreditRefresh(ctx, id)
	return wrapMutation("release provider reset credit refresh", affected, err)
}

func (p *Postgres) UpdateProviderAccount(ctx context.Context, id string, update domain.ProviderAccountUpdate) error {
	affected, err := p.queries.UpdateProviderAccount(ctx, storedb.UpdateProviderAccountParams{
		DisplayName: update.DisplayName, Status: update.Status, FastModeEnabled: update.FastModeEnabled, ID: id,
	})
	return wrapMutation("update provider account", affected, err)
}

func (p *Postgres) GetSettings(ctx context.Context) (domain.Settings, error) {
	row, err := p.queries.GetSettings(ctx)
	return domain.Settings{MaxAPIKeysPerAccount: int(row.MaxApiKeysPerAccount), UpdatedAt: row.UpdatedAt.Time}, wrapDB("get settings", err)
}

func (p *Postgres) UpdateSettings(ctx context.Context, settings domain.Settings) error {
	affected, err := p.queries.UpdateSettings(ctx, int32(settings.MaxAPIKeysPerAccount))
	return wrapMutation("update settings", affected, err)
}

func (p *Postgres) DeleteProviderAccount(ctx context.Context, id string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin provider account deletion", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := p.queries.WithTx(tx)
	if _, err = queries.LockProviderAccountForDeletion(ctx, id); err != nil {
		return wrapDB("lock provider account deletion", err)
	}
	affected, err := queries.DeleteProviderAccount(ctx, id)
	if err != nil {
		return wrapDB("delete provider account", err)
	}
	if affected == 0 {
		return ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit provider account deletion", err)
	}
	return nil
}

func (p *Postgres) UpdateProviderCredentialCAS(ctx context.Context, id string, expectedVersion int, ciphertext []byte, version int) (bool, error) {
	affected, err := p.queries.UpdateProviderCredentialCAS(ctx, storedb.UpdateProviderCredentialCASParams{
		CredentialCiphertext: ciphertext, CredentialVersion: int32(version), ID: id, ExpectedVersion: int32(expectedVersion),
	})
	if err != nil {
		return false, wrapDB("update provider credential", err)
	}
	return affected == 1, nil
}

func (p *Postgres) UpdateProviderStatus(ctx context.Context, id, status string, cooldown *time.Time) error {
	affected, err := p.queries.UpdateProviderStatus(ctx, storedb.UpdateProviderStatusParams{
		Status: status, CooldownUntil: optionalDBTime(cooldown), ID: id,
	})
	return wrapMutation("update provider status", affected, err)
}

func (p *Postgres) SetProviderUsageAllowed(ctx context.Context, id string, allowed bool) error {
	affected, err := p.queries.SetProviderUsageAllowed(ctx, storedb.SetProviderUsageAllowedParams{Allowed: allowed, ID: id})
	return wrapMutation("set provider usage availability", affected, err)
}

func (p *Postgres) SetProviderHealth(ctx context.Context, id, healthStatus, errorCode string, checkedAt, nextCheckAt time.Time) error {
	affected, err := p.queries.SetProviderHealth(ctx, storedb.SetProviderHealthParams{
		HealthStatus: healthStatus, CheckedAt: dbTime(checkedAt), ErrorCode: errorCode,
		NextCheckAt: dbTime(nextCheckAt), ID: id,
	})
	return wrapMutation("set provider health", affected, err)
}

func (p *Postgres) RecordProviderHealthFailure(ctx context.Context, id, errorCode string, checkedAt, nextCheckAt time.Time) error {
	affected, err := p.queries.RecordProviderHealthFailure(ctx, storedb.RecordProviderHealthFailureParams{
		CheckedAt: dbTime(checkedAt), ErrorCode: &errorCode, NextCheckAt: dbTime(nextCheckAt), ID: id,
	})
	return wrapMutation("record provider health failure", affected, err)
}

func (p *Postgres) ReactivateProviderIfCooldownExpired(ctx context.Context, id string, now time.Time) (bool, error) {
	affected, err := p.queries.ReactivateProviderIfCooldownExpired(ctx, storedb.ReactivateProviderIfCooldownExpiredParams{ID: id, Now: dbTime(now)})
	if err != nil {
		return false, wrapDB("reactivate provider after cooldown", err)
	}
	return affected == 1, nil
}

func (p *Postgres) ClaimProviderHealthChecks(ctx context.Context, limit int, now, claimedUntil time.Time) ([]domain.ProviderAccount, error) {
	rows, err := p.queries.ClaimProviderHealthChecks(ctx, storedb.ClaimProviderHealthChecksParams{
		ClaimedUntil: dbTime(claimedUntil), Now: dbTime(now), ClaimLimit: int32(limit),
	})
	if err != nil {
		return nil, wrapDB("claim provider health checks", err)
	}
	var accounts []domain.ProviderAccount
	for _, row := range rows {
		accounts = append(accounts, domain.ProviderAccount{
			ID: row.ID, Provider: row.Provider, CredentialType: row.CredentialType,
			DisplayName: row.DisplayName, CredentialCiphertext: row.CredentialCiphertext,
			CredentialVersion: int(row.CredentialVersion), Status: row.Status, HealthStatus: row.HealthStatus,
			LastCheckedAt: optionalTime(row.LastCheckedAt), LastHealthErrorCode: row.LastHealthErrorCode,
			ConsecutiveFailures: int(row.ConsecutiveHealthFailures), NextHealthCheckAt: optionalTime(row.NextHealthCheckAt),
		})
	}
	return accounts, nil
}

func dbTime(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: true}
}

func optionalDBTime(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return dbTime(*value)
}

func optionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}
