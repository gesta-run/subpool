package adapter

import (
	"context"
	"errors"
	"net/http"

	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

type CopilotClient interface {
	ChatCompletions(context.Context, []byte, http.Header, copilot.Credentials) (*http.Response, error)
	Responses(context.Context, []byte, http.Header, copilot.Credentials) (*http.Response, error)
	SupportsEndpoint(context.Context, string, string, copilot.Credentials) (bool, error)
}

type copilotAdapter struct {
	client CopilotClient
}

func (provider copilotAdapter) RoundTrip(ctx context.Context, request Request, headers http.Header, credentials Credentials, options Options) (*Response, error) {
	if provider.client == nil {
		return nil, errors.New("Copilot provider client is unavailable")
	}
	if request.Kind == "responses_compact" {
		return nil, &RequestError{Code: "unsupported_compaction", Message: "GitHub Copilot does not advertise a standalone Responses compaction endpoint; use client-side summarization or a provider with native compaction support"}
	}
	responses, err := provider.client.SupportsEndpoint(ctx, request.Model, copilot.EndpointResponses, credentials.Copilot)
	if err != nil {
		return nil, err
	}
	chat, err := provider.client.SupportsEndpoint(ctx, request.Model, copilot.EndpointChatCompletions, credentials.Copilot)
	if err != nil {
		return nil, err
	}
	if err = protocol.ValidateCopilotRequest(request.Kind, request.Model, request.Body); err != nil {
		return nil, conversionError(err)
	}
	inboundBytes := len(request.Body)
	if request.Kind == "responses" {
		if responses {
			return provider.native(ctx, request, headers, credentials.Copilot, options.AllowToolFallback, inboundBytes)
		}
		if !chat {
			return nil, ErrUnsupported
		}
		body, tools, err := protocol.ResponsesToCopilotChat(request.Body, options.AllowToolFallback)
		if err != nil {
			return nil, conversionError(err)
		}
		request.Body = body
		return provider.chat(ctx, request, headers, credentials.Copilot, tools, inboundBytes)
	}
	if !chat {
		if !responses {
			return nil, ErrUnsupported
		}
		request.Body, err = protocol.NormalizeCopilotChatHistory(request.Body, request.Model)
		if err != nil {
			return nil, conversionError(err)
		}
		request.Body, err = protocol.ChatToResponses(request.Body)
		if err != nil {
			return nil, conversionError(err)
		}
		return provider.native(ctx, request, headers, credentials.Copilot, options.AllowToolFallback, inboundBytes)
	}
	return provider.chat(ctx, request, headers, credentials.Copilot, nil, inboundBytes)
}

func (provider copilotAdapter) native(ctx context.Context, request Request, headers http.Header, credentials copilot.Credentials, allowFallback bool, inboundBytes int) (*Response, error) {
	body, tools, err := protocol.NormalizeCopilotNativeResponses(request.Body, request.Model, allowFallback)
	if err != nil {
		return nil, conversionError(err)
	}
	response, err := provider.client.Responses(ctx, body, headers, credentials)
	if err != nil {
		return nil, err
	}
	normalizeCopilotPayloadError(response, request, copilot.EndpointResponses, inboundBytes, len(body))
	if response != nil && response.Body != nil {
		protocol.RestoreCopilotNativeResponses(response, tools)
	}
	return &Response{Response: response, format: "responses", tools: tools}, nil
}

func (provider copilotAdapter) chat(ctx context.Context, request Request, headers http.Header, credentials copilot.Credentials, tools map[string]protocol.ToolInfo, inboundBytes int) (*Response, error) {
	body, err := protocol.NormalizeCopilotChatHistory(request.Body, request.Model)
	if err != nil {
		return nil, conversionError(err)
	}
	response, err := provider.client.ChatCompletions(ctx, body, headers, credentials)
	if err != nil {
		return nil, err
	}
	normalizeCopilotPayloadError(response, request, copilot.EndpointChatCompletions, inboundBytes, len(body))
	return &Response{Response: response, format: "chat_completions", tools: tools}, nil
}
