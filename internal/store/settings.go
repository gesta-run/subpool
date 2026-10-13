package store

import (
	"context"
	"fmt"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/store/storedb"
)

// PatchSettings serializes partial updates across all instances sharing the database.
func (p *Postgres) PatchSettings(ctx context.Context, update domain.SettingsUpdate, defaults domain.Settings) (domain.Settings, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.Settings{}, wrapDB("begin settings update", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	queries := p.queries.WithTx(tx)
	row, err := queries.GetSettingsForUpdate(ctx)
	if err != nil {
		return domain.Settings{}, wrapDB("lock settings", err)
	}
	settings, err := update.Apply(domain.Settings{
		MaxAPIKeysPerAccount: int(row.MaxApiKeysPerAccount), MaxRequestBodyBytes: row.MaxRequestBodyBytes,
		MaxInflightRequestBodyBytes: row.MaxInflightRequestBodyBytes, UpdatedAt: row.UpdatedAt.Time,
	}, defaults)
	if err != nil {
		return domain.Settings{}, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	affected, err := queries.UpdateSettings(ctx, storedb.UpdateSettingsParams{
		MaxApiKeysPerAccount: int32(settings.MaxAPIKeysPerAccount), MaxRequestBodyBytes: settings.MaxRequestBodyBytes,
		MaxInflightRequestBodyBytes: settings.MaxInflightRequestBodyBytes,
	})
	if err := wrapMutation("update settings", affected, err); err != nil {
		return domain.Settings{}, err
	}
	updated, err := queries.GetSettings(ctx)
	if err != nil {
		return domain.Settings{}, wrapDB("read updated settings", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Settings{}, wrapDB("commit settings update", err)
	}
	return domain.Settings{
		MaxAPIKeysPerAccount: int(updated.MaxApiKeysPerAccount), MaxRequestBodyBytes: updated.MaxRequestBodyBytes,
		MaxInflightRequestBodyBytes: updated.MaxInflightRequestBodyBytes, UpdatedAt: updated.UpdatedAt.Time,
	}, nil
}
