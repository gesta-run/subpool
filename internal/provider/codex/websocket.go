package codex

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const responsesWebSocketBeta = "responses_websockets=2026-02-06"

func ResponsesWebSocketURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/responses")
	if err != nil {
		return "", fmt.Errorf("parse Codex WebSocket URL: %w", err)
	}
	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
	default:
		return "", fmt.Errorf("unsupported Codex WebSocket URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("Codex WebSocket URL has no host")
	}
	return parsed.String(), nil
}

func ResponsesWebSocketHeaders(downstream http.Header, credentials Credentials, installationID, model string, fastMode bool) http.Header {
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+credentials.AccessToken)
	headers.Set("Originator", "codex_cli_rs")
	headers.Set("User-Agent", defaultUserAgent)
	headers.Set("OpenAI-Beta", responsesWebSocketBeta)
	if credentials.AccountID != "" {
		headers.Set("Chatgpt-Account-Id", credentials.AccountID)
	}
	for _, name := range []string{
		"Version", "X-Codex-Beta-Features", "X-Codex-Turn-Metadata", "X-Client-Request-Id",
		"X-Codex-Window-Id", "Thread-Id", "Session-Id", "X-Openai-Internal-Codex-Responses-Lite",
	} {
		for _, value := range downstream.Values(name) {
			headers.Add(name, value)
		}
	}
	headers = DeviceIdentityHeaders(headers, installationID)
	SetRoutingHint(headers, model, fastMode)
	return headers
}
