package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		(id, provider, credential_type, display_name, subject_hmac, credential_ciphertext, credential_version, status, max_api_keys, quota_snapshot)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,COALESCE($10,'{}'::jsonb))`,
		a.ID, a.Provider, a.CredentialType, a.DisplayName, a.SubjectHMAC, a.CredentialCiphertext, a.CredentialVersion, a.Status, a.MaxAPIKeys, nullableJSON(a.QuotaSnapshot))
	return wrapDB("create provider account", err)
}

func (p *Postgres) ListProviderAccounts(ctx context.Context) ([]domain.ProviderAccount, error) {
	rows, err := p.pool.Query(ctx, `SELECT a.id,a.provider,a.credential_type,a.display_name,a.credential_version,a.status,s.max_api_keys_per_account,
		(SELECT count(*) FROM api_key_account_bindings b JOIN api_keys k ON k.id=b.api_key_id WHERE b.provider_account_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())),
		a.quota_snapshot,a.cooldown_until,a.last_success_at,a.last_failure_at,a.created_at,a.updated_at
		FROM provider_accounts a CROSS JOIN global_settings s WHERE s.singleton ORDER BY a.created_at`)
	if err != nil {
		return nil, wrapDB("list provider accounts", err)
	}
	defer rows.Close()
	var out []domain.ProviderAccount
	for rows.Next() {
		var a domain.ProviderAccount
		if err = rows.Scan(&a.ID, &a.Provider, &a.CredentialType, &a.DisplayName, &a.CredentialVersion, &a.Status, &a.MaxAPIKeys,
			&a.AssignedAPIKeys, &a.QuotaSnapshot, &a.CooldownUntil, &a.LastSuccessAt, &a.LastFailureAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, wrapDB("scan provider account", err)
		}
		out = append(out, a)
	}
	return out, wrapDB("list provider accounts", rows.Err())
}

func (p *Postgres) GetProviderAccount(ctx context.Context, id string) (domain.ProviderAccount, error) {
	var a domain.ProviderAccount
	err := p.pool.QueryRow(ctx, `SELECT id,provider,credential_type,display_name,credential_ciphertext,credential_version,status,max_api_keys,
		quota_snapshot,cooldown_until,last_success_at,last_failure_at,created_at,updated_at FROM provider_accounts WHERE id=$1`, id).
		Scan(&a.ID, &a.Provider, &a.CredentialType, &a.DisplayName, &a.CredentialCiphertext, &a.CredentialVersion, &a.Status, &a.MaxAPIKeys,
			&a.QuotaSnapshot, &a.CooldownUntil, &a.LastSuccessAt, &a.LastFailureAt, &a.CreatedAt, &a.UpdatedAt)
	return a, wrapDB("get provider account", err)
}

func (p *Postgres) UpdateProviderAccount(ctx context.Context, account domain.ProviderAccount) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET display_name=$2,status=$3,
		cooldown_until=CASE WHEN $3='active' THEN NULL ELSE cooldown_until END,updated_at=now() WHERE id=$1`,
		account.ID, account.DisplayName, account.Status)
	return wrapMutation("update provider account", tag.RowsAffected(), err)
}

func (p *Postgres) GetSettings(ctx context.Context) (domain.Settings, error) {
	var settings domain.Settings
	err := p.pool.QueryRow(ctx, `SELECT max_api_keys_per_account,updated_at FROM global_settings WHERE singleton`).Scan(
		&settings.MaxAPIKeysPerAccount, &settings.UpdatedAt)
	return settings, wrapDB("get settings", err)
}

func (p *Postgres) UpdateSettings(ctx context.Context, settings domain.Settings) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return wrapDB("begin settings update", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731242002)`); err != nil {
		return wrapDB("lock settings update", err)
	}
	var value int
	err = tx.QueryRow(ctx, `UPDATE global_settings SET max_api_keys_per_account=$1,updated_at=now()
		WHERE singleton AND $1 >= COALESCE((
			SELECT max(binding_count) FROM (
				SELECT count(k.id) AS binding_count FROM provider_accounts a
				LEFT JOIN api_key_account_bindings b ON b.provider_account_id=a.id
				LEFT JOIN api_keys k ON k.id=b.api_key_id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())
				GROUP BY a.id
			) counts
		),0) RETURNING max_api_keys_per_account`, settings.MaxAPIKeysPerAccount).Scan(&value)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCapacityExhausted
	}
	if err != nil {
		return wrapDB("update settings", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit settings update", err)
	}
	return nil
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
		AND NOT EXISTS (SELECT 1 FROM api_key_account_bindings b WHERE b.provider_account_id=a.id)`, id)
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
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET status=$2,cooldown_until=$3,last_failure_at=CASE WHEN $2='active' THEN last_failure_at ELSE now() END,updated_at=now() WHERE id=$1`, id, status, cooldown)
	return wrapMutation("update provider status", tag.RowsAffected(), err)
}

func (p *Postgres) MarkProviderSuccess(ctx context.Context, id string) error {
	tag, err := p.pool.Exec(ctx, `UPDATE provider_accounts SET status='active',cooldown_until=NULL,last_success_at=now(),updated_at=now() WHERE id=$1`, id)
	return wrapMutation("mark provider success", tag.RowsAffected(), err)
}

func (p *Postgres) CreatePool(ctx context.Context, pool domain.Pool) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO pools(id,name,provider,strategy,model_allowlist) VALUES($1,$2,$3,$4,$5)`, pool.ID, pool.Name, pool.Provider, pool.Strategy, nonNilStrings(pool.ModelAllowlist))
	return wrapDB("create pool", err)
}

func (p *Postgres) UpdatePool(ctx context.Context, pool domain.Pool) error {
	tag, err := p.pool.Exec(ctx, `UPDATE pools SET name=$2,strategy=$3,model_allowlist=$4,updated_at=now() WHERE id=$1`, pool.ID, pool.Name, pool.Strategy, nonNilStrings(pool.ModelAllowlist))
	return wrapMutation("update pool", tag.RowsAffected(), err)
}

func (p *Postgres) ListPools(ctx context.Context) ([]domain.Pool, error) {
	rows, err := p.pool.Query(ctx, `SELECT id,name,provider,strategy,model_allowlist,created_at,updated_at FROM pools ORDER BY created_at`)
	if err != nil {
		return nil, wrapDB("list pools", err)
	}
	defer rows.Close()
	var out []domain.Pool
	for rows.Next() {
		var pool domain.Pool
		if err = rows.Scan(&pool.ID, &pool.Name, &pool.Provider, &pool.Strategy, &pool.ModelAllowlist, &pool.CreatedAt, &pool.UpdatedAt); err != nil {
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
	tag, err := p.pool.Exec(ctx, `INSERT INTO pool_accounts(pool_id,provider_account_id,weight,priority,enabled)
		SELECT $1,$2,$3,$4,$5 FROM pools p JOIN provider_accounts a ON a.id=$2 WHERE p.id=$1 AND p.provider=a.provider
		ON CONFLICT(pool_id,provider_account_id) DO UPDATE SET weight=excluded.weight,priority=excluded.priority,enabled=excluded.enabled`,
		membership.PoolID, membership.ProviderAccountID, membership.Weight, membership.Priority, membership.Enabled)
	return wrapMutation("add pool account", tag.RowsAffected(), err)
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
	accountID, err := selectAccountForUpdate(ctx, tx, key.PoolID, "")
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

func selectAccountForUpdate(ctx context.Context, tx pgx.Tx, poolID, excludeID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT a.id FROM provider_accounts a
		JOIN pool_accounts pa ON pa.provider_account_id=a.id AND pa.pool_id=$1 AND pa.enabled
		WHERE a.id <> COALESCE(NULLIF($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid)
		AND (a.status='active' OR (a.status='cooling_down' AND a.cooldown_until<=now()))
		AND (SELECT count(*) FROM api_key_account_bindings b JOIN api_keys k ON k.id=b.api_key_id
		     WHERE b.provider_account_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now()))
		    < (SELECT max_api_keys_per_account FROM global_settings WHERE singleton)
		ORDER BY (SELECT count(*) FROM api_key_account_bindings b JOIN api_keys k ON k.id=b.api_key_id
		  WHERE b.provider_account_id=a.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())) ASC,
		 pa.priority ASC,
		 random()
		FOR UPDATE OF a LIMIT 1`, poolID, excludeID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrCapacityExhausted
	}
	return id, wrapDB("select provider account", err)
}

func (p *Postgres) ListAPIKeys(ctx context.Context) ([]domain.APIKey, error) {
	rows, err := p.pool.Query(ctx, `SELECT k.id,k.pool_id,b.provider_account_id,k.employee_name,k.key_hint,k.scopes,k.rate_limit,k.expires_at,k.revoked_at,k.last_used_at,k.created_at FROM api_keys k JOIN api_key_account_bindings b ON b.api_key_id=k.id ORDER BY k.created_at DESC`)
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
		p.id,p.name,p.provider,p.strategy,p.model_allowlist,p.created_at,p.updated_at,
		a.id,a.provider,a.credential_type,a.display_name,a.credential_ciphertext,a.credential_version,a.status,a.max_api_keys,a.quota_snapshot,a.cooldown_until,a.last_success_at,a.last_failure_at,a.created_at,a.updated_at,
		pa.enabled
		FROM api_keys k JOIN pools p ON p.id=k.pool_id JOIN api_key_account_bindings b ON b.api_key_id=k.id JOIN provider_accounts a ON a.id=b.provider_account_id
		JOIN pool_accounts pa ON pa.pool_id=p.id AND pa.provider_account_id=a.id
		WHERE k.key_hmac=$1 AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>now())`, digest).Scan(
		&route.Key.ID, &route.Key.PoolID, &route.Key.EmployeeName, &route.Key.KeyHint, &route.Key.Scopes, &route.Key.RateLimit, &route.Key.ExpiresAt, &route.Key.RevokedAt, &route.Key.LastUsedAt, &route.Key.CreatedAt,
		&route.Pool.ID, &route.Pool.Name, &route.Pool.Provider, &route.Pool.Strategy, &route.Pool.ModelAllowlist, &route.Pool.CreatedAt, &route.Pool.UpdatedAt,
		&route.Account.ID, &route.Account.Provider, &route.Account.CredentialType, &route.Account.DisplayName, &route.Account.CredentialCiphertext, &route.Account.CredentialVersion, &route.Account.Status, &route.Account.MaxAPIKeys, &route.Account.QuotaSnapshot, &route.Account.CooldownUntil, &route.Account.LastSuccessAt, &route.Account.LastFailureAt, &route.Account.CreatedAt, &route.Account.UpdatedAt,
		&route.MembershipEnabled)
	return route, wrapDB("resolve API key", err)
}

func (p *Postgres) ResolveSessionAccount(ctx context.Context, keyID string, sessionHash []byte) (domain.ProviderAccount, error) {
	var account domain.ProviderAccount
	err := p.pool.QueryRow(ctx, `SELECT a.id,a.provider,a.credential_type,a.display_name,a.credential_ciphertext,a.credential_version,a.status,a.max_api_keys,
		a.quota_snapshot,a.cooldown_until,a.last_success_at,a.last_failure_at,a.created_at,a.updated_at
		FROM session_bindings s JOIN provider_accounts a ON a.id=s.provider_account_id
		JOIN pool_accounts pa ON pa.pool_id=s.pool_id AND pa.provider_account_id=a.id AND pa.enabled
		WHERE s.api_key_id=$1 AND s.session_hash=$2 AND s.expires_at>now()`, keyID, sessionHash).Scan(
		&account.ID, &account.Provider, &account.CredentialType, &account.DisplayName, &account.CredentialCiphertext, &account.CredentialVersion, &account.Status, &account.MaxAPIKeys,
		&account.QuotaSnapshot, &account.CooldownUntil, &account.LastSuccessAt, &account.LastFailureAt, &account.CreatedAt, &account.UpdatedAt)
	return account, wrapDB("resolve session account", err)
}

func (p *Postgres) SaveSessionBinding(ctx context.Context, keyID, poolID string, sessionHash []byte, accountID string, expiresAt time.Time) error {
	_, err := p.pool.Exec(ctx, `INSERT INTO session_bindings(api_key_id,pool_id,session_hash,provider_account_id,expires_at)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(api_key_id,session_hash) DO UPDATE SET expires_at=GREATEST(session_bindings.expires_at,excluded.expires_at)
		WHERE session_bindings.provider_account_id=excluded.provider_account_id`, keyID, poolID, sessionHash, accountID, expiresAt)
	return wrapDB("save session binding", err)
}

func (p *Postgres) ReassignAPIKey(ctx context.Context, keyID, poolID, excludeID string) (domain.ProviderAccount, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("begin API key reassignment", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(731242002)`); err != nil {
		return domain.ProviderAccount{}, wrapDB("lock API key reassignment", err)
	}
	id, err := selectAccountForUpdate(ctx, tx, poolID, excludeID)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE api_key_account_bindings SET provider_account_id=$2,assigned_at=now() WHERE api_key_id=$1`, keyID, id)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("reassign API key", err)
	}
	var account domain.ProviderAccount
	err = tx.QueryRow(ctx, `SELECT id,provider,credential_type,display_name,credential_ciphertext,credential_version,status,max_api_keys,quota_snapshot,cooldown_until,last_success_at,last_failure_at,created_at,updated_at FROM provider_accounts WHERE id=$1`, id).Scan(
		&account.ID, &account.Provider, &account.CredentialType, &account.DisplayName, &account.CredentialCiphertext, &account.CredentialVersion, &account.Status, &account.MaxAPIKeys, &account.QuotaSnapshot, &account.CooldownUntil, &account.LastSuccessAt, &account.LastFailureAt, &account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("read reassigned account", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProviderAccount{}, wrapDB("commit API key reassignment", err)
	}
	return account, nil
}

func (p *Postgres) TouchAPIKey(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=$1`, id)
	return wrapDB("touch API key", err)
}

func (p *Postgres) AddUsage(ctx context.Context, keyID string, eventHash []byte, day time.Time, input, output int64) error {
	if input < 0 || output < 0 {
		return fmt.Errorf("token counts cannot be negative")
	}
	if len(eventHash) == 0 {
		return fmt.Errorf("usage event hash is required")
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
	_, err = tx.Exec(ctx, `INSERT INTO api_key_usage_daily(api_key_id,usage_date,input_tokens,output_tokens)
		VALUES($1,$2,$3,$4) ON CONFLICT(api_key_id,usage_date) DO UPDATE SET
		input_tokens=api_key_usage_daily.input_tokens+excluded.input_tokens,
		output_tokens=api_key_usage_daily.output_tokens+excluded.output_tokens,updated_at=now()`, keyID, day.UTC(), input, output)
	if err != nil {
		return wrapDB("add usage", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return wrapDB("commit usage transaction", err)
	}
	return nil
}

func (p *Postgres) ListUsage(ctx context.Context, filter domain.UsageFilter) ([]domain.UsageRow, error) {
	rows, err := p.pool.Query(ctx, `SELECT u.api_key_id,k.employee_name,u.usage_date,u.input_tokens,u.output_tokens
		FROM api_key_usage_daily u JOIN api_keys k ON k.id=u.api_key_id
		WHERE (NULLIF($1,'')::uuid IS NULL OR u.api_key_id=NULLIF($1,'')::uuid) AND ($2::timestamptz IS NULL OR u.usage_date >= $2::date) AND ($3::timestamptz IS NULL OR u.usage_date <= $3::date)
		ORDER BY u.usage_date DESC,k.employee_name`, filter.APIKeyID, filter.From, filter.To)
	if err != nil {
		return nil, wrapDB("list usage", err)
	}
	defer rows.Close()
	var out []domain.UsageRow
	for rows.Next() {
		var row domain.UsageRow
		if err = rows.Scan(&row.APIKeyID, &row.EmployeeName, &row.UsageDate, &row.InputTokens, &row.OutputTokens); err != nil {
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
