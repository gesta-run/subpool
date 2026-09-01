package store

import (
	"context"
	"errors"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrCapacityExhausted = errors.New("account capacity exhausted")
	ErrConflict          = errors.New("conflict")
)

type Store interface {
	Ping(context.Context) error
	Close()

	CreateProviderAccount(context.Context, domain.ProviderAccount) error
	ListProviderAccounts(context.Context) ([]domain.ProviderAccount, error)
	GetProviderAccount(context.Context, string) (domain.ProviderAccount, error)
	UpdateProviderAccount(context.Context, domain.ProviderAccount) error
	DeleteProviderAccount(context.Context, string) error
	UpdateProviderCredentialCAS(context.Context, string, int, []byte, int) (bool, error)
	UpdateProviderStatus(context.Context, string, string, *time.Time) error
	MarkProviderSuccess(context.Context, string) error
	GetSettings(context.Context) (domain.Settings, error)
	UpdateSettings(context.Context, domain.Settings) error

	CreatePool(context.Context, domain.Pool) error
	UpdatePool(context.Context, domain.Pool) error
	ListPools(context.Context) ([]domain.Pool, error)
	AddPoolAccount(context.Context, domain.PoolAccount) error

	CreateAPIKeyAndBind(context.Context, domain.APIKey) (string, error)
	ListAPIKeys(context.Context) ([]domain.APIKey, error)
	RevokeAPIKey(context.Context, string) error
	ResolveAPIKey(context.Context, []byte) (domain.KeyRoute, error)
	ResolveSessionAccount(context.Context, string, []byte) (domain.ProviderAccount, error)
	SaveSessionBinding(context.Context, string, string, []byte, string, time.Time) error
	ReassignAPIKey(context.Context, string, string, string) (domain.ProviderAccount, error)
	TouchAPIKey(context.Context, string) error

	AddUsage(context.Context, string, []byte, time.Time, int64, int64) error
	ListUsage(context.Context, domain.UsageFilter) ([]domain.UsageRow, error)
	Audit(context.Context, domain.AuditEvent) error
}
