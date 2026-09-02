package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
)

type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token"`
	AccountID    string    `json:"account_id"`
	Email        string    `json:"email"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type TokenError struct {
	StatusCode int
	Code       string
}

func (e *TokenError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("token endpoint returned status %d with error %s", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("token endpoint returned status %d", e.StatusCode)
}

func IsDefinitiveAuthError(err error) bool {
	var tokenError *TokenError
	return errors.As(err, &tokenError) && (tokenError.StatusCode == http.StatusUnauthorized || tokenError.Code == "invalid_grant")
}

type TokenRefresherConfig struct {
	ClientID   string
	TokenURL   string
	HTTPClient *http.Client
}

type TokenRefresher struct {
	config TokenRefresherConfig
	now    func() time.Time
}

func NewTokenRefresher(config TokenRefresherConfig) *TokenRefresher {
	if config.HTTPClient == nil {
		config.HTTPClient = providerhttp.New()
	}
	return &TokenRefresher{config: config, now: time.Now}
}

func (o *TokenRefresher) Refresh(ctx context.Context, refreshToken string) (Credentials, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Credentials{}, fmt.Errorf("refresh token is required")
	}
	credentials, err := o.tokenRequest(ctx, url.Values{
		"client_id": {o.config.ClientID}, "grant_type": {"refresh_token"}, "refresh_token": {refreshToken},
	})
	if err == nil && credentials.RefreshToken == "" {
		credentials.RefreshToken = refreshToken
	}
	return credentials, err
}

func (o *TokenRefresher) tokenRequest(ctx context.Context, values url.Values) (Credentials, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.config.TokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return Credentials{}, fmt.Errorf("create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := o.config.HTTPClient.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("send token request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Credentials{}, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var failure struct {
			Error any `json:"error"`
		}
		code := ""
		if json.Unmarshal(body, &failure) == nil {
			switch value := failure.Error.(type) {
			case string:
				code = value
			case map[string]any:
				code, _ = value["code"].(string)
			}
		}
		return Credentials{}, &TokenError{StatusCode: resp.StatusCode, Code: code}
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &token); err != nil {
		return Credentials{}, fmt.Errorf("decode token response: %w", err)
	}
	if token.AccessToken == "" {
		return Credentials{}, fmt.Errorf("token response is missing access_token")
	}
	accountID, email := parseIdentity(token.IDToken)
	return Credentials{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, IDToken: token.IDToken, AccountID: accountID, Email: email, ExpiresAt: o.now().Add(time.Duration(token.ExpiresIn) * time.Second)}, nil
}

func parseIdentity(idToken string) (string, string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Email string `json:"email"`
		Auth  struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", ""
	}
	return claims.Auth.AccountID, claims.Email
}
