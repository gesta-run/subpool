package antigravity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const DefaultBaseURL = "https://cloudcode-pa.googleapis.com"
const DefaultRedirectURI = "http://localhost:8085/callback"

type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	ProjectID    string    `json:"project_id"`
	Subject      string    `json:"subject"`
	Email        string    `json:"email"`
}

type Config struct {
	ClientID, ClientSecret, RedirectURI, BaseURL, UserAgent string
	TokenURL, AuthorizeURL, UserInfoURL                     string
	HTTPClient                                              *http.Client
}

type Client struct{ config Config }

func New(config Config) *Client {
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 3 * time.Minute}
	}
	// Never forward OAuth or inference credentials across redirects.
	httpClient := *config.HTTPClient
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	config.HTTPClient = &httpClient
	if config.BaseURL == "" {
		config.BaseURL = DefaultBaseURL
	}
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")
	if config.RedirectURI == "" {
		config.RedirectURI = DefaultRedirectURI
	}
	if config.TokenURL == "" {
		config.TokenURL = "https://oauth2.googleapis.com/token"
	}
	if config.AuthorizeURL == "" {
		config.AuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	}
	if config.UserInfoURL == "" {
		config.UserInfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
	}
	if config.UserAgent == "" {
		config.UserAgent = "antigravity/hub/2.23.0 windows/amd64"
	}
	return &Client{config: config}
}

type HTTPError struct {
	StatusCode int
	OAuthCode  string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Antigravity request failed (status %d)", e.StatusCode)
}
func IsDefinitiveAuthError(err error) bool {
	var e *HTTPError
	return errors.As(err, &e) && (e.StatusCode == http.StatusUnauthorized || e.OAuthCode == "invalid_grant")
}

func (c *Client) Configured() bool { return c.config.ClientID != "" && c.config.ClientSecret != "" }

func (c *Client) do(ctx context.Context, action, token string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.BaseURL+"/v1internal:"+action, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.config.UserAgent)
	return c.config.HTTPClient.Do(req)
}

func decodeResponse(response *http.Response, output any) error {
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&failure)
		return &HTTPError{StatusCode: response.StatusCode, OAuthCode: failure.Error}
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}

func (c *Client) Generate(ctx context.Context, model string, body json.RawMessage, credentials Credentials) (*http.Response, error) {
	if credentials.AccessToken == "" || credentials.ProjectID == "" {
		return nil, errors.New("Antigravity credentials are incomplete")
	}
	wrapper := map[string]any{"project": credentials.ProjectID, "model": model, "request": body,
		"requestId": "agent-" + randomID(), "requestType": "agent", "userAgent": "antigravity"}
	return c.do(ctx, "streamGenerateContent?alt=sse", credentials.AccessToken, wrapper)
}
