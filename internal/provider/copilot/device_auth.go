package copilot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
)

const maxPendingDeviceLogins = 8

type DeviceAuthConfig struct {
	ClientID         string
	DeviceCodeURL    string
	AccessTokenURL   string
	UserURL          string
	TokenExchangeURL string
	HTTPClient       *http.Client
}

type pendingLogin struct {
	cancel context.CancelFunc
	result chan DeviceAuthorizationResult
}

type DeviceAuth struct {
	config  DeviceAuthConfig
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	pending map[string]*pendingLogin
	now     func() time.Time
	wait    func(context.Context, time.Duration) error
}

func NewDeviceAuth(config DeviceAuthConfig) *DeviceAuth {
	if strings.TrimSpace(config.ClientID) == "" {
		config.ClientID = DefaultOAuthClientID
	}
	if config.DeviceCodeURL == "" {
		config.DeviceCodeURL = DefaultDeviceCodeURL
	}
	if config.AccessTokenURL == "" {
		config.AccessTokenURL = DefaultAccessTokenURL
	}
	if config.UserURL == "" {
		config.UserURL = DefaultUserURL
	}
	if config.TokenExchangeURL == "" {
		config.TokenExchangeURL = DefaultTokenExchangeURL
	}
	if config.HTTPClient == nil {
		config.HTTPClient = providerhttp.New()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &DeviceAuth{config: config, ctx: ctx, cancel: cancel, pending: make(map[string]*pendingLogin), now: time.Now, wait: wait}
}

func (d *DeviceAuth) Start(ctx context.Context) (DeviceAuthorization, <-chan DeviceAuthorizationResult, error) {
	if err := ctx.Err(); err != nil {
		return DeviceAuthorization{}, nil, err
	}
	d.mu.Lock()
	if len(d.pending) >= maxPendingDeviceLogins {
		d.mu.Unlock()
		return DeviceAuthorization{}, nil, errors.New("too many Copilot authorizations are already pending")
	}
	d.mu.Unlock()
	code, err := d.requestCode(ctx)
	if err != nil {
		return DeviceAuthorization{}, nil, err
	}
	loginID, err := randomID(24)
	if err != nil {
		return DeviceAuthorization{}, nil, err
	}
	expiresAt := d.now().Add(time.Duration(code.ExpiresIn) * time.Second)
	pollCtx, cancel := context.WithDeadline(d.ctx, expiresAt)
	pending := &pendingLogin{cancel: cancel, result: make(chan DeviceAuthorizationResult, 1)}
	d.mu.Lock()
	if len(d.pending) >= maxPendingDeviceLogins {
		d.mu.Unlock()
		cancel()
		return DeviceAuthorization{}, nil, errors.New("too many Copilot authorizations are already pending")
	}
	d.pending[loginID] = pending
	d.mu.Unlock()
	go d.poll(pollCtx, loginID, code, pending)
	return DeviceAuthorization{
		LoginID: loginID, UserCode: code.UserCode, VerificationURL: code.VerificationURI, ExpiresAt: expiresAt,
	}, pending.result, nil
}

func (d *DeviceAuth) Cancel(loginID string) {
	d.mu.Lock()
	pending, ok := d.pending[loginID]
	if ok {
		delete(d.pending, loginID)
	}
	d.mu.Unlock()
	if ok {
		pending.cancel()
	}
}

func (d *DeviceAuth) Close() {
	d.cancel()
	d.mu.Lock()
	pending := d.pending
	d.pending = make(map[string]*pendingLogin)
	d.mu.Unlock()
	for _, login := range pending {
		login.cancel()
	}
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type deviceTokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (d *DeviceAuth) requestCode(ctx context.Context) (deviceCodeResponse, error) {
	values := url.Values{"client_id": {d.config.ClientID}, "scope": {"read:user"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.config.DeviceCodeURL, strings.NewReader(values.Encode()))
	if err != nil {
		return deviceCodeResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.config.HTTPClient.Do(req)
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("request GitHub device code: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return deviceCodeResponse{}, fmt.Errorf("read GitHub device code response: %w", err)
	}
	var code deviceCodeResponse
	if resp.StatusCode != http.StatusOK {
		return code, &HTTPError{
			StatusCode: resp.StatusCode,
			Operation:  "GitHub device authorization",
			Detail:     safeHTTPErrorDetail(body),
		}
	}
	if err = json.Unmarshal(body, &code); err != nil {
		return code, fmt.Errorf("decode GitHub device code: %w", err)
	}
	if code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI == "" || code.ExpiresIn <= 0 {
		return code, errors.New("GitHub returned an invalid device authorization response")
	}
	verificationURL, parseErr := url.Parse(code.VerificationURI)
	if parseErr != nil || verificationURL.Scheme != "https" || !strings.EqualFold(verificationURL.Hostname(), "github.com") {
		return code, errors.New("GitHub returned an invalid verification URL")
	}
	if code.Interval < 1 {
		code.Interval = 5
	}
	return code, nil
}

func (d *DeviceAuth) poll(ctx context.Context, loginID string, code deviceCodeResponse, pending *pendingLogin) {
	interval := time.Duration(code.Interval) * time.Second
	var result DeviceAuthorizationResult
	for {
		if err := d.wait(ctx, interval); err != nil {
			result.Err = err
			break
		}
		token, err := d.pollToken(ctx, code.DeviceCode)
		if err != nil {
			result.Err = err
			break
		}
		switch token.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "":
			result.Credentials, result.Err = d.credentials(ctx, token.AccessToken)
		default:
			detail := token.ErrorDescription
			if detail == "" {
				detail = token.Error
			}
			result.Err = &authorizationError{operation: "GitHub device authorization failed", detail: detail}
		}
		break
	}
	d.mu.Lock()
	current, ok := d.pending[loginID]
	if ok && current == pending {
		delete(d.pending, loginID)
	}
	d.mu.Unlock()
	if ok {
		pending.result <- result
	}
	close(pending.result)
	pending.cancel()
}

func (d *DeviceAuth) pollToken(ctx context.Context, deviceCode string) (deviceTokenResponse, error) {
	values := url.Values{
		"client_id": {d.config.ClientID}, "device_code": {deviceCode},
		"grant_type": {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.config.AccessTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return deviceTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := d.config.HTTPClient.Do(req)
	if err != nil {
		return deviceTokenResponse{}, fmt.Errorf("poll GitHub device authorization: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return deviceTokenResponse{}, fmt.Errorf("read GitHub device authorization response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return deviceTokenResponse{}, &HTTPError{
			StatusCode: resp.StatusCode,
			Operation:  "GitHub device authorization",
			Detail:     safeHTTPErrorDetail(body, deviceCode),
		}
	}
	var payload deviceTokenResponse
	if err = json.Unmarshal(body, &payload); err != nil {
		return deviceTokenResponse{}, fmt.Errorf("decode GitHub device authorization: %w", err)
	}
	if payload.Error == "" && payload.AccessToken == "" {
		return deviceTokenResponse{}, errors.New("GitHub device authorization returned no access token")
	}
	payload.Error = normalizeErrorDetail(payload.Error, deviceCode)
	payload.ErrorDescription = normalizeErrorDetail(payload.ErrorDescription, deviceCode)
	return payload, nil
}

func (d *DeviceAuth) credentials(ctx context.Context, githubToken string) (Credentials, error) {
	if _, err := exchangeToken(ctx, d.config.HTTPClient, d.config.TokenExchangeURL, githubToken, d.now()); err != nil {
		return Credentials{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.config.UserURL, nil)
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := d.config.HTTPClient.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("read GitHub user: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Credentials{}, fmt.Errorf("read GitHub user response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Credentials{}, &HTTPError{
			StatusCode: resp.StatusCode,
			Operation:  "GitHub user lookup",
			Detail:     safeHTTPErrorDetail(body, githubToken),
		}
	}
	var user struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	if err = json.Unmarshal(body, &user); err != nil {
		return Credentials{}, fmt.Errorf("decode GitHub user: %w", err)
	}
	if user.ID <= 0 || strings.TrimSpace(user.Login) == "" {
		return Credentials{}, errors.New("GitHub user response is missing identity")
	}
	return Credentials{GitHubToken: githubToken, Login: user.Login, UserID: user.ID, Email: user.Email}, nil
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func randomID(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
