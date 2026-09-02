package store

import (
	"context"
	"errors"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrNoEligibleAccount = errors.New("no eligible account")
	ErrConflict          = errors.New("conflict")
)

type Store interface {
	Ping(context.Context) error
	Close()

	CreateProviderAccount(context.Context, domain.ProviderAccount) error
	ListProviderAccounts(context.Context) ([]domain.ProviderAccount, error)
	ListPoolProviderAccounts(context.Context, string) ([]domain.ProviderAccount, error)
	GetProviderAccount(context.Context, string) (domain.ProviderAccount, error)
	UpdateProviderAccount(context.Context, domain.ProviderAccount) error
	UpdateProviderDetails(context.Context, string, string, []byte) error
	DeleteProviderAccount(context.Context, string) error
	UpdateProviderCredentialCAS(context.Context, string, int, []byte, int) (bool, error)
	UpdateProviderStatus(context.Context, string, string, *time.Time) error
	SetProviderHealth(context.Context, string, string, string, time.Time, time.Time) error
	RecordProviderHealthFailure(context.Context, string, string, time.Time, time.Time) error
	ClaimProviderHealthChecks(context.Context, int, time.Time, time.Time) ([]domain.ProviderAccount, error)
	GetProviderResetCredits(context.Context, string) ([]byte, *time.Time, error)
	SetProviderResetCredits(context.Context, string, []byte, time.Time) error
	ClaimProviderResetCreditRefresh(context.Context, string, time.Time, time.Time) (bool, error)
	ReleaseProviderResetCreditRefresh(context.Context, string) error
	GetSettings(context.Context) (domain.Settings, error)
	UpdateSettings(context.Context, domain.Settings) error
	RecordAdminLoginAttempt(context.Context, []string, bool, time.Time) (bool, error)
	CreateAdminSession(context.Context, []byte, time.Time) error
	AdminSessionActive(context.Context, []byte, time.Time) (bool, error)
	RevokeAdminSession(context.Context, []byte, time.Time) error

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
	ReassignAPIKey(context.Context, string, string, []string) (domain.ProviderAccount, error)
	AllowAPIKeyRequest(context.Context, string, int, time.Time) (bool, error)
	RecordRequestSuccess(context.Context, string, string, time.Time) error

	AddUsage(context.Context, string, []byte, string, time.Time, int64, int64) error
	ListUsage(context.Context, domain.UsageFilter) ([]domain.UsageRow, error)
	Audit(context.Context, domain.AuditEvent) error
}
