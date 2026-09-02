package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (p *Postgres) CreatePool(ctx context.Context, pool domain.Pool) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin pool creation", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO pools(id,name,provider) VALUES($1,$2,$3)`, pool.ID, pool.Name, pool.Provider); err != nil {
		return wrapDB("create pool", err)
	}
	for _, membership := range pool.Accounts {
		tag, membershipErr := tx.Exec(ctx, `INSERT INTO pool_accounts(pool_id,provider_account_id,weight,priority,enabled)
			SELECT $1,$2,$3,$4,$5 FROM provider_accounts a WHERE a.id=$2`,
			pool.ID, membership.ProviderAccountID, membership.Weight, membership.Priority, membership.Enabled)
		if membershipErr = wrapMutation("add initial pool account", tag.RowsAffected(), membershipErr); membershipErr != nil {
			return membershipErr
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit pool creation", err)
	}
	return nil
}

func (p *Postgres) UpdatePool(ctx context.Context, pool domain.Pool) error {
	tag, err := p.pool.Exec(ctx, `UPDATE pools SET name=$2,updated_at=now() WHERE id=$1`, pool.ID, pool.Name)
	return wrapMutation("update pool", tag.RowsAffected(), err)
}

func (p *Postgres) ListPools(ctx context.Context) ([]domain.Pool, error) {
	rows, err := p.pool.Query(ctx, `SELECT id,name,provider,created_at,updated_at FROM pools ORDER BY created_at`)
	if err != nil {
		return nil, wrapDB("list pools", err)
	}
	defer rows.Close()
	var out []domain.Pool
	for rows.Next() {
		var pool domain.Pool
		if err = rows.Scan(&pool.ID, &pool.Name, &pool.Provider, &pool.CreatedAt, &pool.UpdatedAt); err != nil {
			return nil, wrapDB("scan pool", err)
		}
		out = append(out, pool)
	}
	if err = rows.Err(); err != nil {
		return nil, wrapDB("list pools", err)
	}
	poolIndexes := make(map[string]int, len(out))
	for i := range out {
		poolIndexes[out[i].ID] = i
	}
	memberRows, queryErr := p.pool.Query(ctx, `SELECT pool_id,provider_account_id,weight,priority,enabled FROM pool_accounts ORDER BY priority,provider_account_id`)
	if queryErr != nil {
		return nil, wrapDB("list pool accounts", queryErr)
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var member domain.PoolAccount
		if queryErr = memberRows.Scan(&member.PoolID, &member.ProviderAccountID, &member.Weight, &member.Priority, &member.Enabled); queryErr != nil {
			return nil, wrapDB("scan pool account", queryErr)
		}
		if index, ok := poolIndexes[member.PoolID]; ok {
			out[index].Accounts = append(out[index].Accounts, member)
		}
	}
	if queryErr = memberRows.Err(); queryErr != nil {
		return nil, wrapDB("list pool accounts", queryErr)
	}
	return out, nil
}

func (p *Postgres) AddPoolAccount(ctx context.Context, membership domain.PoolAccount) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin pool account addition", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var poolProvider, accountProvider string
	if err = tx.QueryRow(ctx, `SELECT provider FROM pools WHERE id=$1 FOR UPDATE`, membership.PoolID).Scan(&poolProvider); err != nil {
		return wrapDB("lock pool", err)
	}
	if err = tx.QueryRow(ctx, `SELECT provider FROM provider_accounts WHERE id=$1`, membership.ProviderAccountID).Scan(&accountProvider); err != nil {
		return wrapDB("read provider account", err)
	}
	tag, err := tx.Exec(ctx, `INSERT INTO pool_accounts(pool_id,provider_account_id,weight,priority,enabled)
		VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(pool_id,provider_account_id) DO UPDATE SET weight=excluded.weight,priority=excluded.priority,enabled=excluded.enabled`,
		membership.PoolID, membership.ProviderAccountID, membership.Weight, membership.Priority, membership.Enabled)
	if err = wrapMutation("add pool account", tag.RowsAffected(), err); err != nil {
		return err
	}
	if poolProvider != accountProvider && poolProvider != domain.ProviderMixed {
		if _, err = tx.Exec(ctx, `UPDATE pools SET provider=$2,updated_at=now() WHERE id=$1`, membership.PoolID, domain.ProviderMixed); err != nil {
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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731242002)`); err != nil {
		return "", wrapDB("lock API key assignment", err)
	}
	accountID, err := selectAccountForUpdate(ctx, tx, key.PoolID, nil)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO api_keys(id,pool_id,employee_name,key_hmac,key_hint,scopes,rate_limit,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, key.ID, key.PoolID, key.EmployeeName, key.KeyHMAC, key.KeyHint, nonNilStrings(key.Scopes), key.RateLimit, key.ExpiresAt)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO api_key_account_bindings(api_key_id,provider_account_id) VALUES($1,$2)`, key.ID, accountID)
	}
	if err != nil {
		return "", wrapDB("create API key", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return "", wrapDB("commit API key assignment", err)
	}
	return accountID, nil
}

func selectAccountForUpdate(ctx context.Context, tx pgx.Tx, poolID string, excludeIDs []string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT a.id FROM provider_accounts a
		JOIN pool_accounts pa ON pa.provider_account_id=a.id AND pa.pool_id=$1 AND pa.enabled
		CROSS JOIN global_settings settings
		WHERE NOT (a.id::text = ANY(COALESCE($2::text[], ARRAY[]::text[])))
			AND (a.status='active' OR (a.status='cooling_down' AND a.cooldown_until<=now()))
			AND COALESCE(NULLIF(a.health_status,''),'unknown')!='unhealthy'
			AND (SELECT count(*) FROM api_key_account_bindings b JOIN api_keys k ON k.id=b.api_key_id
				WHERE b.provider_account_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())) < settings.max_api_keys_per_account
			ORDER BY pa.priority ASC,
				(SELECT count(*)::numeric / GREATEST(pa.weight,1) FROM api_key_account_bindings b JOIN api_keys k ON k.id=b.api_key_id
		  WHERE b.provider_account_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())) ASC,
			 random()
		FOR UPDATE OF a LIMIT 1`, poolID, excludeIDs).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoEligibleAccount
	}
	return id, wrapDB("select provider account", err)
}

func (p *Postgres) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	rows, err := p.pool.Query(ctx, `SELECT k.id,k.pool_id,COALESCE(b.provider_account_id::text,''),k.employee_name,k.key_hint,k.scopes,k.rate_limit,k.expires_at,k.revoked_at,k.last_used_at,k.created_at
		FROM api_keys k LEFT JOIN api_key_account_bindings b ON b.api_key_id=k.id ORDER BY k.created_at DESC`)
	if err != nil {
		return nil, wrapDB("list API keys", err)
	}
	defer rows.Close()
	var out []domain.APIKey
	for rows.Next() {
		var key domain.APIKey
		if err = rows.Scan(&key.ID, &key.PoolID, &key.ProviderAccountID, &key.EmployeeName, &key.KeyHint, &key.Scopes, &key.RateLimit, &key.ExpiresAt, &key.RevokedAt, &key.LastUsedAt, &key.CreatedAt); err != nil {
			return nil, wrapDB("scan API key", err)
		}
		out = append(out, key)
	}
	return out, wrapDB("list API keys", rows.Err())
}

func (p *Postgres) RevokeAPIKey(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE api_keys SET revoked_at=now() WHERE id=$1 AND revoked_at IS NULL`, id)
	return wrapMutation("revoke API key", tag.RowsAffected(), err)
}

func (p *Postgres) ResolveAPIKey(ctx context.Context, digest []byte) (domain.KeyRoute, error) {
	var route domain.KeyRoute
	err := p.pool.QueryRow(ctx, `SELECT k.id,k.pool_id,k.employee_name,k.key_hint,k.scopes,k.rate_limit,k.expires_at,k.revoked_at,k.last_used_at,k.created_at,
		p.id,p.name,p.provider,p.created_at,p.updated_at,
		a.id,a.provider,a.credential_type,a.display_name,a.credential_ciphertext,a.credential_version,a.status,a.health_status,a.quota_snapshot,a.cooldown_until,a.last_success_at,a.last_failure_at,a.created_at,a.updated_at,
		pa.enabled
		FROM api_keys k JOIN pools p ON p.id=k.pool_id JOIN api_key_account_bindings b ON b.api_key_id=k.id JOIN provider_accounts a ON a.id=b.provider_account_id
		JOIN pool_accounts pa ON pa.pool_id=p.id AND pa.provider_account_id=a.id
		WHERE k.key_hmac=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())`, digest).Scan(
		&route.Key.ID, &route.Key.PoolID, &route.Key.EmployeeName, &route.Key.KeyHint, &route.Key.Scopes, &route.Key.RateLimit, &route.Key.ExpiresAt, &route.Key.RevokedAt, &route.Key.LastUsedAt, &route.Key.CreatedAt,
		&route.Pool.ID, &route.Pool.Name, &route.Pool.Provider, &route.Pool.CreatedAt, &route.Pool.UpdatedAt,
		&route.Account.ID, &route.Account.Provider, &route.Account.CredentialType, &route.Account.DisplayName, &route.Account.CredentialCiphertext, &route.Account.CredentialVersion, &route.Account.Status, &route.Account.HealthStatus, &route.Account.QuotaSnapshot, &route.Account.CooldownUntil, &route.Account.LastSuccessAt, &route.Account.LastFailureAt, &route.Account.CreatedAt, &route.Account.UpdatedAt,
		&route.MembershipEnabled)
	return route, wrapDB("resolve API key", err)
}

func (p *Postgres) ResolveSessionAccount(ctx context.Context, keyID string, sessionHash []byte) (domain.ProviderAccount, error) {
	var account domain.ProviderAccount
	err := p.pool.QueryRow(ctx, `SELECT a.id,a.provider,a.credential_type,a.display_name,a.credential_ciphertext,a.credential_version,a.status,
		a.health_status,a.quota_snapshot,a.cooldown_until,a.last_success_at,a.last_failure_at,a.created_at,a.updated_at
		FROM session_bindings s JOIN provider_accounts a ON a.id=s.provider_account_id
		JOIN pool_accounts pa ON pa.pool_id=s.pool_id AND pa.provider_account_id=a.id AND pa.enabled
		WHERE s.api_key_id=$1 AND s.session_hash=$2 AND s.expires_at>now()`, keyID, sessionHash).Scan(
		&account.ID, &account.Provider, &account.CredentialType, &account.DisplayName, &account.CredentialCiphertext, &account.CredentialVersion, &account.Status,
		&account.HealthStatus, &account.QuotaSnapshot, &account.CooldownUntil, &account.LastSuccessAt, &account.LastFailureAt, &account.CreatedAt, &account.UpdatedAt)
	return account, wrapDB("resolve session account", err)
}

func (p *Postgres) SaveSessionBinding(ctx context.Context, keyID, poolID string, sessionHash []byte, accountID string, expiresAt time.Time) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO session_bindings(api_key_id,pool_id,session_hash,provider_account_id,expires_at)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(api_key_id,session_hash) DO UPDATE SET expires_at=GREATEST(session_bindings.expires_at,excluded.expires_at)
		WHERE session_bindings.provider_account_id=excluded.provider_account_id`, keyID, poolID, sessionHash, accountID, expiresAt)
	return wrapDB("save session binding", err)
}

func (p *Postgres) ReassignAPIKey(ctx context.Context, keyID, poolID string, excludeIDs []string) (domain.ProviderAccount, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("begin API key reassignment", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731242002)`); err != nil {
		return domain.ProviderAccount{}, wrapDB("lock API key reassignment", err)
	}
	id, err := selectAccountForUpdate(ctx, tx, poolID, excludeIDs)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE api_key_account_bindings SET provider_account_id=$2,assigned_at=now() WHERE api_key_id=$1`, keyID, id)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("reassign API key", err)
	}
	var account domain.ProviderAccount
	err = tx.QueryRow(ctx, `SELECT id,provider,credential_type,display_name,credential_ciphertext,credential_version,status,health_status,quota_snapshot,cooldown_until,last_success_at,last_failure_at,created_at,updated_at FROM provider_accounts WHERE id=$1`, id).Scan(
		&account.ID, &account.Provider, &account.CredentialType, &account.DisplayName, &account.CredentialCiphertext, &account.CredentialVersion, &account.Status, &account.HealthStatus, &account.QuotaSnapshot, &account.CooldownUntil, &account.LastSuccessAt, &account.LastFailureAt, &account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("read reassigned account", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProviderAccount{}, wrapDB("commit API key reassignment", err)
	}
	return account, nil
}

func (p *Postgres) RecordRequestSuccess(ctx context.Context, accountID, keyID string, occurredAt time.Time) error {
	_, err := p.pool.Exec(ctx, `WITH account AS (
		UPDATE provider_accounts SET status='active',cooldown_until=NULL,last_success_at=$3,
			health_status='healthy',last_checked_at=$3,last_health_error_code=NULL,consecutive_health_failures=0,
			next_health_check_at=$3+interval '5 minutes',updated_at=now() WHERE id=$1 RETURNING id
	) UPDATE api_keys SET last_used_at=$3 WHERE id=$2 AND EXISTS(SELECT 1 FROM account)`, accountID, keyID, occurredAt)
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
	tag, err := tx.Exec(ctx, `INSERT INTO usage_event_dedup(api_key_id,event_hash) VALUES($1,$2) ON CONFLICT DO NOTHING`, keyID, eventHash)
	if err != nil {
		return wrapDB("deduplicate usage event", err)
	}
	if tag.RowsAffected() == 0 {
		if err = tx.Commit(ctx); err != nil {
			return wrapDB("commit duplicate usage event", err)
		}
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO api_key_usage_daily(api_key_id,usage_date,model,input_tokens,output_tokens)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(api_key_id,usage_date,model) DO UPDATE SET
		input_tokens=api_key_usage_daily.input_tokens+excluded.input_tokens,
		output_tokens=api_key_usage_daily.output_tokens+excluded.output_tokens,updated_at=now()`, keyID, day.UTC(), model, input, output)
	if err != nil {
		return wrapDB("add usage", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit usage transaction", err)
	}
	return nil
}

func (p *Postgres) ListUsage(ctx context.Context, filter domain.UsageFilter) ([]domain.UsageRow, error) {
	rows, err := p.pool.Query(ctx, `SELECT u.api_key_id,k.employee_name,k.key_hint,u.model,u.usage_date,u.input_tokens,u.output_tokens
		FROM api_key_usage_daily u JOIN api_keys k ON k.id=u.api_key_id
		WHERE (NULLIF($1,'')::uuid IS NULL OR u.api_key_id=NULLIF($1,'')::uuid) AND ($2::timestamptz IS NULL OR u.usage_date >= $2::date) AND ($3::timestamptz IS NULL OR u.usage_date <= $3::date)
		ORDER BY u.usage_date DESC,k.employee_name,u.model`, filter.APIKeyID, filter.From, filter.To)
	if err != nil {
		return nil, wrapDB("list usage", err)
	}
	defer rows.Close()
	var out []domain.UsageRow
	for rows.Next() {
		var row domain.UsageRow
		if err = rows.Scan(&row.APIKeyID, &row.EmployeeName, &row.KeyHint, &row.Model, &row.UsageDate, &row.InputTokens, &row.OutputTokens); err != nil {
			return nil, wrapDB("scan usage", err)
		}
		out = append(out, row)
	}
	return out, wrapDB("list usage", rows.Err())
}

func (p *Postgres) Audit(ctx context.Context, event domain.AuditEvent) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO audit_events(actor,action,target_type,target_id,result) VALUES($1,$2,$3,$4,$5)`, event.Actor, event.Action, event.TargetType, event.TargetID, event.Result)
	return wrapDB("write audit event", err)
}

func nullableJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
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
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return fmt.Errorf("%s: %w", action, err)
}
