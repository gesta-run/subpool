package adapter

import (
	"context"
	"errors"
	"net/http"

	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"github.com/gesta-run/subpool/internal/provider/codex"
)

type CodexClient interface {
	Responses(context.Context, []byte, http.Header, codex.Credentials) (*http.Response, error)
	ImageGenerations(context.Context, []byte, http.Header, codex.Credentials) (*http.Response, error)
	ImageEdits(context.Context, []byte, http.Header, codex.Credentials) (*http.Response, error)
}

type codexAdapter struct {
	client CodexClient
}

func (provider codexAdapter) RoundTrip(ctx context.Context, request Request, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	if provider.client == nil {
		return nil, errors.New("Codex provider client is unavailable")
	}
	installationID, err := codex.InstallationID(options.AccountID)
	if err != nil {
		return nil, err
	}
	headers = codex.DeviceIdentityHeaders(headers, installationID)
	if request.Kind == "responses_compact" {
		return provider.compact(ctx, request, headers, credentials.Codex)
	}
	if request.Kind == "image_generations" || request.Kind == "image_edits" {
		return provider.image(ctx, request, headers, credentials.Codex)
	}
	if request.Kind == "chat_completions" {
		request.Body, err = protocol.ChatToResponses(request.Body)
		if err != nil {
			return nil, conversionError(err)
		}
	}
	body, err := protocol.NormalizeCodexRequest(request.Body, installationID, options.FastMode)
	if err != nil {
		return nil, err
	}
	codex.SetRoutingHint(headers, request.Model, options.FastMode)
	response, err := provider.client.Responses(ctx, body, headers, credentials.Codex)
	if err != nil {
		return nil, err
	}
	return &Response{Response: response, format: "responses"}, nil
}

type codexCompactClient interface {
	Compact(context.Context, []byte, http.Header, codex.Credentials) (*http.Response, error)
}

func (provider codexAdapter) compact(ctx context.Context, request Request, headers http.Header, credentials codex.Credentials) (*Response, error) {
	client, ok := provider.client.(codexCompactClient)
	if !ok {
		return nil, unsupportedCompaction()
	}
	response, err := client.Compact(ctx, request.Body, headers, credentials)
	if err != nil {
		return nil, err
	}
	if endpointUnavailable(response) {
		closeEndpointResponse(response)
		return nil, unsupportedCompaction()
	}
	return &Response{Response: response, format: "responses_compact"}, nil
}

func (provider codexAdapter) image(ctx context.Context, request Request, headers http.Header, credentials codex.Credentials) (*Response, error) {
	var response *http.Response
	var err error
	if request.Kind == "image_edits" {
		response, err = provider.client.ImageEdits(ctx, request.Body, headers, credentials)
	} else {
		response, err = provider.client.ImageGenerations(ctx, request.Body, headers, credentials)
	}
	if err != nil {
		return nil, err
	}
	return &Response{Response: response, format: request.Kind}, nil
}
