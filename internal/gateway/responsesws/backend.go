package responsesws

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
)

const maxProviderAttempts = 8

var (
	ErrRequestBodyTooLarge = errors.New("request body is too large")
	ErrRequestBodyCapacity = errors.New("request body capacity is unavailable")
)

type RequestError struct {
	Status  int
	Message string
	Code    string
}

type RequestBodyState struct {
	MaxBytes         int64
	ReadTimeout      time.Duration
	InflightBytes    int64
	MaxInflightBytes int64
}

type RetryReason string

const (
	RetryUnavailable RetryReason = "unavailable"
	RetryAuth        RetryReason = "authentication"
	RetryRefresh     RetryReason = "refresh"
	RetryRateLimit   RetryReason = "rate_limit"
	RetryInvalid     RetryReason = "invalid_request"
	RetryTransport   RetryReason = "transport"
	RetryProvider5xx RetryReason = "provider_5xx"
)

type Backend interface {
	Authenticate(context.Context, string, string) (domain.KeyRoute, []byte, *RequestError)
	AllowAPIKeyRequest(context.Context, domain.KeyRoute) *RequestError
	RoutingStore() RoutingStore
	Now() time.Time
	AccountHealthy(domain.ProviderAccount) bool
	CodexCredentials(domain.ProviderAccount) (codex.Credentials, error)
	RefreshAccount(context.Context, domain.ProviderAccount) (domain.ProviderAccount, error)
	RecordRefreshFailure(context.Context, string, error) bool
	RecordHealthFailure(context.Context, string, string)
	RetryAfter(http.Header) time.Time
	AttemptBridge(context.Context, http.Header, domain.KeyRoute, domain.ProviderAccount, string, []byte) (*http.Response, RetryReason, bool)
	CompleteTurn(string, string, string, string, string, int64, int64)
	NormalizeCodexRequest([]byte, domain.ProviderAccount) ([]byte, *RequestError)
	ReadRequestBody(io.Reader) ([]byte, func(), error)
	RequestBodyState() RequestBodyState
}

type RoutingStore interface {
	ResolveAPIKey(context.Context, []byte) (domain.KeyRoute, error)
	ResolvePinnedAPIKey(context.Context, []byte, string, string) (domain.KeyRoute, error)
	ResolveSessionAccount(context.Context, string, []byte) (domain.ProviderAccount, error)
	ReassignAPIKey(context.Context, string, string, []string) (domain.ProviderAccount, error)
	UpdateProviderStatus(context.Context, string, string, *time.Time) error
	SetProviderUsageAllowed(context.Context, string, bool) error
}

func scopeAllowed(scopes []string, required string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, scope := range scopes {
		if scope == "*" || scope == required {
			return true
		}
	}
	return false
}

func refreshableCredentials(account domain.ProviderAccount) bool {
	return account.CredentialType == "" || account.CredentialType == domain.CredentialSubscription
}
