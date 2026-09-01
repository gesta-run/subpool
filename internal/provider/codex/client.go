package codex

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
)

const defaultUserAgent = "codex-tui/0.146.0 (subpool)"

type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
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
	for _, name := range []string{"Version", "X-Codex-Beta-Features", "X-Codex-Turn-Metadata", "X-Client-Request-Id", "X-Codex-Window-Id", "Thread-Id", "Session-Id", "X-Openai-Internal-Codex-Responses-Lite"} {
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
