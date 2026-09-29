package copilot

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gesta-run/subpool/internal/jsonobject"
	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
)

const modelEndpointCacheTTL = 5 * time.Minute

type ClientConfig struct {
	APIBase          string
	TokenExchangeURL string
	EntitlementsURL  string
	HTTPClient       *http.Client
}

type tokenEntry struct {
	token      accessToken
	refreshing chan struct{}
}

type modelEntry struct {
	models     map[string]Model
	expiresAt  time.Time
	refreshing chan struct{}
}

type Client struct {
	apiBase          string
	tokenExchangeURL string
	entitlementsURL  string
	httpClient       *http.Client
	now              func() time.Time
	mu               sync.Mutex
	tokens           map[string]*tokenEntry
	models           map[string]*modelEntry
}

func NewClient(config ClientConfig) *Client {
	if strings.TrimSpace(config.APIBase) == "" {
		config.APIBase = DefaultAPIBase
	}
	if strings.TrimSpace(config.TokenExchangeURL) == "" {
		config.TokenExchangeURL = DefaultTokenExchangeURL
	}
	if strings.TrimSpace(config.EntitlementsURL) == "" {
		config.EntitlementsURL = DefaultEntitlementsURL
	}
	if config.HTTPClient == nil {
		config.HTTPClient = providerhttp.New()
	}
	return &Client{
		apiBase: strings.TrimRight(config.APIBase, "/"), tokenExchangeURL: config.TokenExchangeURL, entitlementsURL: config.EntitlementsURL,
		httpClient: config.HTTPClient, now: time.Now, tokens: make(map[string]*tokenEntry), models: make(map[string]*modelEntry),
	}
}

func (c *Client) ChatCompletions(ctx context.Context, body []byte, source http.Header, credentials Credentials) (*http.Response, error) {
	return c.request(ctx, http.MethodPost, EndpointChatCompletions, sanitizeRequestBody(body), source, credentials)
}

func (c *Client) Responses(ctx context.Context, body []byte, source http.Header, credentials Credentials) (*http.Response, error) {
	headers := source.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Accept", "text/event-stream")
	return c.request(ctx, http.MethodPost, EndpointResponses, sanitizeRequestBody(body), headers, credentials)
}

func (c *Client) Models(ctx context.Context, credentials Credentials) (*http.Response, error) {
	return c.request(ctx, http.MethodGet, "/models", nil, nil, credentials)
}

func (c *Client) Validate(ctx context.Context, credentials Credentials) error {
	_, err := c.token(ctx, credentials)
	return err
}

func (c *Client) SupportsEndpoint(ctx context.Context, modelID, endpoint string, credentials Credentials) (bool, error) {
	models, err := c.cachedModels(ctx, credentials)
	if err != nil {
		return false, err
	}
	model, ok := models[modelID]
	return ok && model.SupportsEndpoint(endpoint), nil
}

func (c *Client) cachedModels(ctx context.Context, credentials Credentials) (map[string]Model, error) {
	key := tokenKey(credentials.GitHubToken)
	for {
		c.mu.Lock()
		entry := c.models[key]
		if entry != nil && entry.refreshing == nil && c.now().Before(entry.expiresAt) {
			models := entry.models
			c.mu.Unlock()
			return models, nil
		}
		if entry != nil && entry.refreshing != nil {
			ready := entry.refreshing
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ready:
				continue
			}
		}
		entry = &modelEntry{refreshing: make(chan struct{})}
		c.models[key] = entry
		c.mu.Unlock()

		models, err := c.fetchModels(ctx, credentials)
		c.mu.Lock()
		if err == nil {
			entry.models = models
			entry.expiresAt = c.now().Add(modelEndpointCacheTTL)
		}
		close(entry.refreshing)
		entry.refreshing = nil
		if err != nil {
			delete(c.models, key)
		}
		c.mu.Unlock()
		return models, err
	}
}

func (c *Client) fetchModels(ctx context.Context, credentials Credentials) (map[string]Model, error) {
	resp, err := c.Models(ctx, credentials)
	if err != nil {
		return nil, err
	}
	models, err := DecodeModels(resp)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Model, len(models))
	for _, model := range models {
		if identifier := model.Identifier(); identifier != "" {
			byID[identifier] = model
		}
	}
	return byID, nil
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, source http.Header, credentials Credentials) (*http.Response, error) {
	token, err := c.token(ctx, credentials)
	if err != nil {
		return nil, err
	}
	resp, err := c.doRequest(ctx, method, path, body, source, token)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	c.invalidate(credentials.GitHubToken)
	token, err = c.token(ctx, credentials)
	if err != nil {
		return nil, err
	}
	resp, err = c.doRequest(ctx, method, path, body, source, token)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		c.invalidate(credentials.GitHubToken)
	}
	return resp, err
}

func (c *Client) doRequest(ctx context.Context, method, path string, body []byte, source http.Header, token accessToken) (*http.Response, error) {
	apiBase := token.apiBase
	if apiBase == "" {
		apiBase = c.apiBase
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.value)
	req.Header.Set("Accept", "application/json")
	if source != nil && source.Get("Accept") != "" {
		req.Header.Set("Accept", source.Get("Accept"))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setEditorHeaders(req.Header)
	req.Header.Set("copilot-integration-id", "vscode-chat")
	req.Header.Set("openai-intent", "conversation-panel")
	req.Header.Set("X-Initiator", "user")
	req.Header.Set("x-request-id", requestID())
	return c.httpClient.Do(req)
}

func (c *Client) token(ctx context.Context, credentials Credentials) (accessToken, error) {
	if strings.TrimSpace(credentials.GitHubToken) == "" {
		return accessToken{}, ErrCredentialsIncomplete
	}
	key := tokenKey(credentials.GitHubToken)
	for {
		c.mu.Lock()
		entry := c.tokens[key]
		if entry != nil && entry.refreshing == nil && c.now().Before(entry.token.refreshAt) {
			token := entry.token
			c.mu.Unlock()
			return token, nil
		}
		if entry != nil && entry.refreshing != nil {
			ready := entry.refreshing
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return accessToken{}, ctx.Err()
			case <-ready:
				continue
			}
		}
		entry = &tokenEntry{refreshing: make(chan struct{})}
		c.tokens[key] = entry
		c.mu.Unlock()

		token, err := exchangeToken(ctx, c.httpClient, c.tokenExchangeURL, credentials.GitHubToken, c.now())
		c.mu.Lock()
		entry.token = token
		close(entry.refreshing)
		entry.refreshing = nil
		if err != nil {
			delete(c.tokens, key)
		}
		c.mu.Unlock()
		return token, err
	}
}

func (c *Client) invalidate(githubToken string) {
	c.mu.Lock()
	delete(c.tokens, tokenKey(githubToken))
	c.mu.Unlock()
}

func DecodeModels(resp *http.Response) ([]Model, error) {
	if resp == nil || resp.Body == nil {
		return nil, errors.New("Copilot model response is empty")
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return nil, &HTTPError{StatusCode: resp.StatusCode, Operation: "Copilot models"}
	}
	var payload struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode Copilot models: %w", err)
	}
	return payload.Data, nil
}

func setEditorHeaders(header http.Header) {
	header.Set("editor-version", "vscode/1.98.1")
	header.Set("editor-plugin-version", "copilot-chat/0.26.7")
	header.Set("User-Agent", "GitHubCopilotChat/0.26.7")
	header.Set("x-github-api-version", "2026-06-01")
	header.Set("x-vscode-user-agent-library-version", "electron-fetch")
}

func sanitizeRequestBody(body []byte) []byte {
	value, err := jsonobject.Parse(body)
	if err != nil {
		return body
	}
	for _, field := range []string{"client_metadata", "x-codex-installation-id", "x-codex-turn-metadata"} {
		value.Delete(field)
	}
	return value.Bytes()
}

func tokenKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func requestID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("subpool-%d", time.Now().UnixNano())
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}
