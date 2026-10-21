package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gesta-run/subpool/internal/adapter/antigravitywire"
	"github.com/gesta-run/subpool/internal/adapter/copilotcompact"
	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/copilot"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

var ErrUnsupported = errors.New("provider does not support the requested endpoint")

type Request struct {
	Kind         string
	Model        string
	Body         []byte
	IncludeUsage bool
}

type Options struct {
	AccountID         string
	FastMode          bool
	AllowToolFallback bool
	// InternalUsage records inference performed inside an adapter, even if the
	// overall operation fails. Ordinary inference is observed by the renderer.
	InternalUsage     func(input, output int64)
	CompactionState   *copilotcompact.State
	compactionCovered []string
	compactionStarted func()
	compactionSampled func()
}

type Credentials struct {
	Antigravity antigravity.Credentials
	Codex       codex.Credentials
	Copilot     copilot.Credentials
	Compatible  openaicompat.Credentials
}

type Provider interface {
	RoundTrip(context.Context, Request, http.Header, Credentials, Options) (*Response, error)
}

type Clients struct {
	Antigravity           AntigravityClient
	AntigravitySignatures *antigravitywire.Signatures
	AntigravityLimits     *antigravitywire.ModelLimits
	Codex                 CodexClient
	Compatible            CompatibleClient
	Copilot               CopilotClient
}

type RequestError struct {
	Code    string
	Message string
}

func (err *RequestError) Error() string { return err.Message }

// ResultError is terminal: replaying a multi-step operation could duplicate
// already-completed inference. Its message must not include provider content.
type ResultError struct {
	Code    string
	Message string
}

func (err *ResultError) Error() string { return err.Message }

func For(provider string, clients Clients) (Provider, error) {
	var implementation Provider
	switch provider {
	case "", domain.ProviderCodex:
		implementation = codexAdapter{client: clients.Codex}
	case domain.ProviderCopilot:
		implementation = copilotAdapter{client: clients.Copilot}
	case domain.ProviderOpenAICompatible:
		implementation = compatibleAdapter{client: clients.Compatible}
	case domain.ProviderAntigravity:
		implementation = antigravityAdapter{client: clients.Antigravity, signatures: clients.AntigravitySignatures, limits: clients.AntigravityLimits}
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
	return compactionAdapter{Provider: implementation, copilot: provider == domain.ProviderCopilot}, nil
}

func conversionError(err error) error {
	var requestError *protocol.RequestError
	if errors.As(err, &requestError) {
		return &RequestError{Code: requestError.Code, Message: requestError.Message}
	}
	return &RequestError{Code: "unsupported_request", Message: err.Error()}
}
