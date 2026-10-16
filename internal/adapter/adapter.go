package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"github.com/gesta-run/subpool/internal/domain"
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
	InternalUsage func(input, output int64)
}

type Credentials struct {
	Codex      codex.Credentials
	Copilot    copilot.Credentials
	Compatible openaicompat.Credentials
}

type Provider interface {
	RoundTrip(context.Context, Request, http.Header, Credentials, Options) (*Response, error)
}

type Clients struct {
	Codex      CodexClient
	Compatible CompatibleClient
	Copilot    CopilotClient
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
	switch provider {
	case "", domain.ProviderCodex:
		return codexAdapter{client: clients.Codex}, nil
	case domain.ProviderCopilot:
		return copilotAdapter{client: clients.Copilot}, nil
	case domain.ProviderOpenAICompatible:
		return compatibleAdapter{client: clients.Compatible}, nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}

func conversionError(err error) error {
	var requestError *protocol.RequestError
	if errors.As(err, &requestError) {
		return &RequestError{Code: requestError.Code, Message: requestError.Message}
	}
	return &RequestError{Code: "unsupported_request", Message: err.Error()}
}
