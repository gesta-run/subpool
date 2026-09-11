package codex

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
)

const defaultUserAgent = "codex-tui/0.146.0 (subpool)"

const RoutingHintHeader = "X-Codex-Routing-Hint"

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = providerhttp.New()
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{baseURL: baseURL, httpClient: httpClient}
}

func SetRoutingHint(headers http.Header, model string, fastMode bool) {
	for name := range headers {
		if strings.EqualFold(strings.TrimSpace(name), RoutingHintHeader) {
			delete(headers, name)
		}
	}
	model = strings.TrimSpace(model)
	if !validRoutingHintModel(model) {
		return
	}
	hint := "model=" + model
	if fastMode {
		hint += ";tier=priority"
	}
	headers.Set(RoutingHintHeader, hint)
}

func validRoutingHintModel(model string) bool {
	if model == "" {
		return false
	}
	for _, value := range model {
		if value < 0x21 || value > 0x7e || value == ';' || value == '=' {
			return false
		}
	}
	return true
}

func (c *Client) Responses(ctx context.Context, body []byte, downstream http.Header, credentials Credentials) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Codex request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", defaultUserAgent)
	if credentials.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", credentials.AccountID)
	}
	for _, name := range []string{"Version", "X-Codex-Beta-Features", "X-Codex-Installation-Id", "X-Codex-Turn-Metadata", "X-Client-Request-Id", "X-Codex-Window-Id", "Thread-Id", "Session-Id", "X-Openai-Internal-Codex-Responses-Lite", RoutingHintHeader} {
		if value := downstream.Get(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send Codex request: %w", err)
	}
	return resp, nil
}
