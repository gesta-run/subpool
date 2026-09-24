package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/store/storedb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func (p *Postgres) CreatePool(ctx context.Context, pool domain.Pool) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin pool creation", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := p.queries.WithTx(tx)
	if err = queries.CreatePool(ctx, storedb.CreatePoolParams{ID: pool.ID, Name: pool.Name, Provider: pool.Provider}); err != nil {
		return wrapDB("create pool", err)
	}
	for _, membership := range pool.Accounts {
		affected, membershipErr := queries.AddInitialPoolAccount(ctx, storedb.AddInitialPoolAccountParams{
			PoolID: pool.ID, ProviderAccountID: membership.ProviderAccountID,
			Weight: int32(membership.Weight), Priority: int32(membership.Priority), Enabled: membership.Enabled,
		})
		if membershipErr = wrapMutation("add initial pool account", affected, membershipErr); membershipErr != nil {
			return membershipErr
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit pool creation", err)
	}
	return nil
}

func (p *Postgres) UpdatePool(ctx context.Context, pool domain.Pool) error {
	affected, err := p.queries.UpdatePool(ctx, storedb.UpdatePoolParams{Name: pool.Name, ID: pool.ID})
	return wrapMutation("update pool", affected, err)
}

func (p *Postgres) ListPools(ctx context.Context) ([]domain.Pool, error) {
	rows, err := p.queries.ListPools(ctx)
	if err != nil {
		return nil, wrapDB("list pools", err)
	}
	var pools []domain.Pool
	poolIndexes := make(map[string]int, len(rows))
	for _, row := range rows {
		poolIndexes[row.ID] = len(pools)
		pools = append(pools, domain.Pool{
			ID: row.ID, Name: row.Name, Provider: row.Provider,
			CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		})
	}
	memberships, err := p.queries.ListPoolAccounts(ctx)
	if err != nil {
		return nil, wrapDB("list pool accounts", err)
	}
	for _, row := range memberships {
		if index, ok := poolIndexes[row.PoolID]; ok {
			pools[index].Accounts = append(pools[index].Accounts, domain.PoolAccount{
				PoolID: row.PoolID, ProviderAccountID: row.ProviderAccountID,
				Weight: int(row.Weight), Priority: int(row.Priority), Enabled: row.Enabled,
			})
		}
	}
	return pools, nil
}

func (p *Postgres) AddPoolAccount(ctx context.Context, membership domain.PoolAccount) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin pool account addition", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := p.queries.WithTx(tx)
	poolProvider, err := queries.LockPoolForUpdate(ctx, membership.PoolID)
	if err != nil {
		return wrapDB("lock pool", err)
	}
	accountProvider, err := queries.GetProviderAccountProvider(ctx, membership.ProviderAccountID)
	if err != nil {
		return wrapDB("read provider account", err)
	}
	affected, err := queries.UpsertPoolAccount(ctx, storedb.UpsertPoolAccountParams{
		PoolID: membership.PoolID, ProviderAccountID: membership.ProviderAccountID,
		Weight: int32(membership.Weight), Priority: int32(membership.Priority), Enabled: membership.Enabled,
	})
	if err = wrapMutation("add pool account", affected, err); err != nil {
		return err
	}
	if poolProvider != accountProvider && poolProvider != domain.ProviderMixed {
		if err = queries.MarkPoolMixed(ctx, membership.PoolID); err != nil {
			return wrapDB("mark pool mixed", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit pool account addition", err)
	}
	return nil
}

func (p *Postgres) CreateAPIKeyAndBind(ctx context.Context, key domain.APIKey) (string, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return "", wrapDB("begin API key assignment", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := p.queries.WithTx(tx)
	if err = queries.LockPoolAssignments(ctx, key.PoolID); err != nil {
		return "", wrapDB("lock API key assignment", err)
	}
	accountID, err := selectAccountForUpdate(ctx, queries, key.PoolID, nil)
	if err != nil {
		return "", err
	}
	err = queries.CreateAPIKey(ctx, storedb.CreateAPIKeyParams{
		ID: key.ID, PoolID: key.PoolID, EmployeeName: key.EmployeeName,
		KeyHmac: key.KeyHMAC, KeyHint: key.KeyHint, Scopes: nonNilStrings(key.Scopes),
		RateLimit: int32(key.RateLimit), ExpiresAt: optionalDBTime(key.ExpiresAt),
	})
	if err == nil {
		err = queries.BindAPIKey(ctx, storedb.BindAPIKeyParams{ApiKeyID: key.ID, ProviderAccountID: accountID})
	}
	if err != nil {
		return "", wrapDB("create API key", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", wrapDB("commit API key assignment", err)
	}
	return accountID, nil
}

func selectAccountForUpdate(ctx context.Context, queries *storedb.Queries, poolID string, excludeIDs []string) (string, error) {
	excluded := append([]string(nil), excludeIDs...)
	for {
		id, err := queries.SelectAccountForUpdate(ctx, storedb.SelectAccountForUpdateParams{
			PoolID: poolID, ExcludeIds: nonNilStrings(excluded),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNoEligibleAccount
		}
		if err != nil {
			return "", wrapDB("select provider account", err)
		}
		hasCapacity, err := queries.ProviderAccountHasCapacity(ctx, id)
		if err != nil {
			return "", wrapDB("check provider account capacity", err)
		}
		if hasCapacity {
			return id, nil
		}
		excluded = append(excluded, id)
	}
}

func (p *Postgres) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	rows, err := p.queries.ListAPIKeys(ctx)
	if err != nil {
		return nil, wrapDB("list API keys", err)
	}
	var keys []domain.APIKey
	for _, row := range rows {
		keys = append(keys, domain.APIKey{
			ID: row.ID, PoolID: row.PoolID, ProviderAccountID: row.ProviderAccountID,
			EmployeeName: row.EmployeeName, KeyHint: row.KeyHint, Scopes: row.Scopes,
			RateLimit: int(row.RateLimit), ExpiresAt: optionalTime(row.ExpiresAt),
			RevokedAt: optionalTime(row.RevokedAt), LastUsedAt: optionalTime(row.LastUsedAt), CreatedAt: row.CreatedAt.Time,
		})
	}
	return keys, nil
}

func (p *Postgres) RevokeAPIKey(ctx context.Context, id string) error {
	affected, err := p.queries.RevokeAPIKey(ctx, id)
	return wrapMutation("revoke API key", affected, err)
}

func (p *Postgres) ResolveAPIKey(ctx context.Context, digest []byte) (domain.KeyRoute, error) {
	row, err := p.queries.ResolveAPIKey(ctx, digest)
	if err != nil {
		return domain.KeyRoute{}, wrapDB("resolve API key", err)
	}
	return domain.KeyRoute{
		Key: domain.APIKey{
			ID: row.KeyID, PoolID: row.KeyPoolID, EmployeeName: row.EmployeeName, KeyHint: row.KeyHint,
			Scopes: row.Scopes, RateLimit: int(row.RateLimit), ExpiresAt: optionalTime(row.KeyExpiresAt),
			RevokedAt: optionalTime(row.RevokedAt), LastUsedAt: optionalTime(row.LastUsedAt), CreatedAt: row.KeyCreatedAt.Time,
		},
		Pool: domain.Pool{ID: row.PoolID, Name: row.PoolName, Provider: row.PoolProvider, CreatedAt: row.PoolCreatedAt.Time, UpdatedAt: row.PoolUpdatedAt.Time},
		Account: routeAccount(row.AccountID, row.AccountProvider, row.CredentialType, row.DisplayName,
			row.CredentialCiphertext, row.CredentialVersion, row.Status, row.FastModeEnabled, row.HealthStatus,
			row.QuotaSnapshot, row.CooldownUntil, row.LastSuccessAt, row.LastFailureAt, row.AccountCreatedAt, row.AccountUpdatedAt),
		MembershipEnabled: row.MembershipEnabled,
	}, nil
}

func (p *Postgres) ResolvePinnedAPIKey(ctx context.Context, digest []byte, poolID, accountID string) (domain.KeyRoute, error) {
	row, err := p.queries.ResolvePinnedAPIKey(ctx, storedb.ResolvePinnedAPIKeyParams{
		AccountID: accountID, KeyHmac: digest, PoolID: poolID,
	})
	if err == nil {
		return domain.KeyRoute{
			Key: domain.APIKey{
				ID: row.KeyID, PoolID: row.KeyPoolID, EmployeeName: row.EmployeeName, KeyHint: row.KeyHint,
				Scopes: row.Scopes, RateLimit: int(row.RateLimit), ExpiresAt: optionalTime(row.KeyExpiresAt),
				RevokedAt: optionalTime(row.RevokedAt), LastUsedAt: optionalTime(row.LastUsedAt), CreatedAt: row.KeyCreatedAt.Time,
			},
			Pool: domain.Pool{ID: row.PoolID, Name: row.PoolName, Provider: row.PoolProvider, CreatedAt: row.PoolCreatedAt.Time, UpdatedAt: row.PoolUpdatedAt.Time},
			Account: routeAccount(row.AccountID, row.AccountProvider, row.CredentialType, row.DisplayName,
				row.CredentialCiphertext, row.CredentialVersion, row.Status, row.FastModeEnabled, row.HealthStatus,
				row.QuotaSnapshot, row.CooldownUntil, row.LastSuccessAt, row.LastFailureAt, row.AccountCreatedAt, row.AccountUpdatedAt),
			MembershipEnabled: row.MembershipEnabled,
		}, nil
	}
	resolvedErr := wrapDB("resolve pinned API key", err)
	if !errors.Is(resolvedErr, ErrNotFound) {
		return domain.KeyRoute{}, resolvedErr
	}
	valid, checkErr := p.queries.PinnedAPIKeyValid(ctx, storedb.PinnedAPIKeyValidParams{KeyHmac: digest, PoolID: poolID})
	if checkErr != nil {
		return domain.KeyRoute{}, wrapDB("check pinned API key", checkErr)
	}
	if valid {
		return domain.KeyRoute{}, ErrPinnedUnavailable
	}
	return domain.KeyRoute{}, ErrNotFound
}

func (p *Postgres) ResolveSessionAccount(ctx context.Context, keyID string, sessionHash []byte) (domain.ProviderAccount, error) {
	row, err := p.queries.ResolveSessionAccount(ctx, storedb.ResolveSessionAccountParams{ApiKeyID: keyID, SessionHash: sessionHash})
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("resolve session account", err)
	}
	return routeAccount(row.ID, row.Provider, row.CredentialType, row.DisplayName,
		row.CredentialCiphertext, row.CredentialVersion, row.Status, row.FastModeEnabled,
		row.HealthStatus, row.QuotaSnapshot, row.CooldownUntil, row.LastSuccessAt,
		row.LastFailureAt, row.CreatedAt, row.UpdatedAt), nil
}

func (p *Postgres) SaveSessionBinding(ctx context.Context, keyID, poolID string, sessionHash []byte, accountID string, expiresAt time.Time) error {
	err := p.queries.SaveSessionBinding(ctx, storedb.SaveSessionBindingParams{
		ApiKeyID: keyID, PoolID: poolID, SessionHash: sessionHash,
		ProviderAccountID: accountID, ExpiresAt: dbTime(expiresAt),
	})
	return wrapDB("save session binding", err)
}

func (p *Postgres) ReassignAPIKey(ctx context.Context, keyID, poolID string, excludeIDs []string) (domain.ProviderAccount, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("begin API key reassignment", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := p.queries.WithTx(tx)
	if err = queries.LockPoolAssignments(ctx, poolID); err != nil {
		return domain.ProviderAccount{}, wrapDB("lock API key reassignment", err)
	}
	id, err := selectAccountForUpdate(ctx, queries, poolID, excludeIDs)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if err = queries.ReassignAPIKey(ctx, storedb.ReassignAPIKeyParams{ProviderAccountID: id, ApiKeyID: keyID}); err != nil {
		return domain.ProviderAccount{}, wrapDB("reassign API key", err)
	}
	row, err := queries.GetRoutableProviderAccount(ctx, id)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("read reassigned account", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProviderAccount{}, wrapDB("commit API key reassignment", err)
	}
	return routeAccount(row.ID, row.Provider, row.CredentialType, row.DisplayName,
		row.CredentialCiphertext, row.CredentialVersion, row.Status, row.FastModeEnabled,
		row.HealthStatus, row.QuotaSnapshot, row.CooldownUntil, row.LastSuccessAt,
		row.LastFailureAt, row.CreatedAt, row.UpdatedAt), nil
}

func (p *Postgres) RecordRequestSuccess(ctx context.Context, accountID, keyID string, occurredAt time.Time) error {
	err := p.queries.RecordRequestSuccess(ctx, storedb.RecordRequestSuccessParams{
		OccurredAt: dbTime(occurredAt), ApiKeyID: keyID, AccountID: accountID,
	})
	return wrapDB("record request success", err)
}

func (p *Postgres) AddUsage(ctx context.Context, keyID string, eventHash []byte, model string, day time.Time, input, output int64) error {
	if input < 0 || output < 0 {
		return fmt.Errorf("token counts cannot be negative")
	}
	if len(eventHash) == 0 {
		return fmt.Errorf("usage event hash is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "unknown"
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin usage transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := p.queries.WithTx(tx)
	affected, err := queries.DeduplicateUsageEvent(ctx, storedb.DeduplicateUsageEventParams{ApiKeyID: keyID, EventHash: eventHash})
	if err != nil {
		return wrapDB("deduplicate usage event", err)
	}
	if affected == 0 {
		if err = tx.Commit(ctx); err != nil {
			return wrapDB("commit duplicate usage event", err)
		}
		return nil
	}
	err = queries.AddUsage(ctx, storedb.AddUsageParams{
		ApiKeyID: keyID, UsageDate: pgtype.Date{Time: day.UTC(), Valid: true},
		Model: model, InputTokens: input, OutputTokens: output,
	})
	if err != nil {
		return wrapDB("add usage", err)
	}
	err = queries.UpdateAPIKeyActivity(ctx, storedb.UpdateAPIKeyActivityParams{OccurredAt: dbTime(day.UTC()), ID: keyID})
	if err != nil {
		return wrapDB("update API key activity", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit usage transaction", err)
	}
	return nil
}

func (p *Postgres) ListUsageSummary(ctx context.Context, filter domain.UsageSummaryFilter) ([]domain.UsageSummary, error) {
	rows, err := p.queries.ListUsageSummary(ctx, storedb.ListUsageSummaryParams{
		AfterTotal: filter.AfterTotal, AfterApiKey: filter.AfterAPIKey, AfterModel: filter.AfterModel,
		PageLimit: int32(filter.Limit), ApiKeyID: filter.APIKeyID,
		FromTime: optionalDBTime(filter.From), ToTime: optionalDBTime(filter.To),
	})
	if err != nil {
		return nil, wrapDB("list usage summary", err)
	}
	var usage []domain.UsageSummary
	for _, row := range rows {
		usage = append(usage, domain.UsageSummary{
			APIKeyID: row.ApiKeyID, EmployeeName: row.EmployeeName, KeyHint: row.KeyHint,
			Model: row.Model, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		})
	}
	return usage, nil
}

func (p *Postgres) GetUsageTotals(ctx context.Context, filter domain.UsageSummaryFilter) (domain.UsageTotals, error) {
	row, err := p.queries.GetUsageTotals(ctx, storedb.GetUsageTotalsParams{
		ApiKeyID: filter.APIKeyID, FromTime: optionalDBTime(filter.From), ToTime: optionalDBTime(filter.To),
	})
	return domain.UsageTotals{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens}, wrapDB("get usage totals", err)
}

func (p *Postgres) ListTopUsageKeys(ctx context.Context, filter domain.UsageSummaryFilter, limit int) ([]domain.UsageKeySummary, error) {
	rows, err := p.queries.ListTopUsageKeys(ctx, storedb.ListTopUsageKeysParams{
		ApiKeyID: filter.APIKeyID, FromTime: optionalDBTime(filter.From),
		ToTime: optionalDBTime(filter.To), TopLimit: int32(limit),
	})
	if err != nil {
		return nil, wrapDB("list top usage keys", err)
	}
	var keys []domain.UsageKeySummary
	for _, row := range rows {
		keys = append(keys, domain.UsageKeySummary{
			APIKeyID: row.ApiKeyID, EmployeeName: row.EmployeeName, KeyHint: row.KeyHint,
			InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		})
	}
	return keys, nil
}

func (p *Postgres) Audit(ctx context.Context, event domain.AuditEvent) error {
	err := p.queries.WriteAuditEvent(ctx, storedb.WriteAuditEventParams{
		Actor: event.Actor, Action: event.Action, TargetType: event.TargetType,
		TargetID: event.TargetID, Result: event.Result,
	})
	return wrapDB("write audit event", err)
}

func routeAccount(id, provider, credentialType, displayName string, ciphertext []byte, version int32,
	status string, fastMode bool, healthStatus string, quota []byte, cooldown, lastSuccess,
	lastFailure, createdAt, updatedAt pgtype.Timestamptz,
) domain.ProviderAccount {
	return domain.ProviderAccount{
		ID: id, Provider: provider, CredentialType: credentialType, DisplayName: displayName,
		CredentialCiphertext: ciphertext, CredentialVersion: int(version), Status: status,
		FastModeEnabled: fastMode, HealthStatus: healthStatus, QuotaSnapshot: quota,
		CooldownUntil: optionalTime(cooldown), LastSuccessAt: optionalTime(lastSuccess),
		LastFailureAt: optionalTime(lastFailure), CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time,
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func wrapMutation(action string, affected int64, err error) error {
	if err != nil {
		return wrapDB(action, err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func wrapDB(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23505") {
		return ErrConflict
	}
	return fmt.Errorf("%s: %w", action, err)
}
