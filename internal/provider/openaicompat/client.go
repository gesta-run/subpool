package openaicompat

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
)

type Credentials struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = providerhttp.New()
	}
	return &Client{httpClient: httpClient}
}

func (c *Client) Responses(ctx context.Context, body []byte, source http.Header, credentials Credentials) (*http.Response, error) {
	return c.post(ctx, "/responses", body, source, credentials)
}

func (c *Client) ChatCompletions(ctx context.Context, body []byte, source http.Header, credentials Credentials) (*http.Response, error) {
	return c.post(ctx, "/chat/completions", body, source, credentials)
}

func (c *Client) Models(ctx context.Context, credentials Credentials) (*http.Response, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(credentials.BaseURL), "/")
	if baseURL == "" || strings.TrimSpace(credentials.APIKey) == "" {
		return nil, errors.New("OpenAI-compatible credentials are incomplete")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	req.Header.Set("Accept", "application/json")
	return c.httpClient.Do(req)
}

func (c *Client) post(ctx context.Context, path string, body []byte, source http.Header, credentials Credentials) (*http.Response, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(credentials.BaseURL), "/")
	if baseURL == "" || strings.TrimSpace(credentials.APIKey) == "" {
		return nil, errors.New("OpenAI-compatible credentials are incomplete")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+credentials.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", source.Get("Accept"))
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	return c.httpClient.Do(req)
}
