package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type CompatibleClient interface {
	Responses(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error)
	ChatCompletions(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error)
}

type compatibleAdapter struct {
	client CompatibleClient
}

func (provider compatibleAdapter) RoundTrip(ctx context.Context, request Request, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	if provider.client == nil {
		return nil, errors.New("OpenAI-compatible provider client is unavailable")
	}
	if request.Kind == "responses_compact" {
		return provider.compact(ctx, request, headers, credentials.Compatible)
	}
	var response *http.Response
	var err error
	if request.Kind == "chat_completions" {
		response, err = provider.client.ChatCompletions(ctx, request.Body, headers, credentials.Compatible)
	} else {
		response, err = provider.client.Responses(ctx, protocol.ForceStream(request.Body), headers, credentials.Compatible)
	}
	if err != nil {
		return nil, err
	}
	if !endpointUnavailable(response) {
		return &Response{Response: response, format: request.Kind}, nil
	}
	closeEndpointResponse(response)
	return provider.alternate(ctx, request, headers, credentials.Compatible)
}

func (provider compatibleAdapter) alternate(ctx context.Context, request Request, headers http.Header, credentials openaicompat.Credentials) (*Response, error) {
	var body []byte
	var tools map[string]protocol.ToolInfo
	var response *http.Response
	var err error
	format := "responses"
	if request.Kind == "chat_completions" {
		body, err = protocol.ChatToResponses(request.Body)
		if err == nil {
			response, err = provider.client.Responses(ctx, body, headers, credentials)
		}
	} else {
		format = "chat_completions"
		body, tools, err = protocol.ResponsesToChat(request.Body, false)
		if err == nil {
			response, err = provider.client.ChatCompletions(ctx, body, headers, credentials)
		}
	}
	if err != nil {
		if response == nil && body == nil {
			return nil, conversionError(err)
		}
		return nil, err
	}
	if endpointUnavailable(response) {
		closeEndpointResponse(response)
		return nil, &RequestError{Code: "unsupported_provider_endpoint", Message: "provider supports neither the requested endpoint nor its alternate protocol"}
	}
	return &Response{Response: response, format: format, tools: tools}, nil
}

type compatibleCompactClient interface {
	Compact(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error)
}

func (provider compatibleAdapter) compact(ctx context.Context, request Request, headers http.Header, credentials openaicompat.Credentials) (*Response, error) {
	client, ok := provider.client.(compatibleCompactClient)
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

func unsupportedCompaction() error {
	return &RequestError{Code: "unsupported_compaction", Message: "provider does not support standalone Responses compaction; use client-side summarization or a provider with native compaction support"}
}

type replayBody struct {
	io.Reader
	io.Closer
}

// Never reinterpret model, authentication, rate-limit, or validation failures
// as missing endpoint capabilities. Keep the error body available to the caller.
func endpointUnavailable(response *http.Response) bool {
	if response == nil {
		return false
	}
	switch response.StatusCode {
	case http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	case http.StatusNotFound:
		if response.Body == nil {
			return true
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		response.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(raw), response.Body), Closer: response.Body}
		if err != nil {
			return false
		}
		var payload struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(raw, &payload)
		return !strings.Contains(strings.ToLower(payload.Error.Code+" "+payload.Error.Message+" "+string(raw)), "model")
	default:
		return false
	}
}

func closeEndpointResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		_ = response.Body.Close()
	}
}
