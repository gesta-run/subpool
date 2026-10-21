package antigravity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var errOAuthNotConfigured = errors.New("configure both SUBPOOL_ANTIGRAVITY_CLIENT_ID and SUBPOOL_ANTIGRAVITY_CLIENT_SECRET before authorizing Google")

type Authorization struct {
	LoginID   string    `json:"login_id"`
	URL       string    `json:"authorization_url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type pendingLogin struct {
	verifier string
	expires  time.Time
}
type OAuth struct {
	client  *Client
	mu      sync.Mutex
	pending map[string]pendingLogin
}

func NewOAuth(client *Client) *OAuth {
	return &OAuth{client: client, pending: make(map[string]pendingLogin)}
}

func randomID() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("secure random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(value[:])
}

func (o *OAuth) Start() (Authorization, error) {
	if !o.client.Configured() {
		return Authorization{}, errOAuthNotConfigured
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for key, entry := range o.pending {
		if time.Now().After(entry.expires) {
			delete(o.pending, key)
		}
	}
	if len(o.pending) >= 128 {
		return Authorization{}, errors.New("too many pending Google authorizations; try again later")
	}
	state, verifier := randomID(), randomID()
	expires := time.Now().Add(10 * time.Minute)
	o.pending[state] = pendingLogin{verifier: verifier, expires: expires}
	digest := sha256.Sum256([]byte(verifier))
	params := url.Values{"client_id": {o.client.config.ClientID}, "redirect_uri": {DefaultRedirectURI},
		"response_type": {"code"}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state},
		"code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])},
		"scope": {"openid email profile https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/cclog https://www.googleapis.com/auth/experimentsandconfigs"}}
	return Authorization{LoginID: state, URL: o.client.config.AuthorizeURL + "?" + params.Encode(), ExpiresAt: expires}, nil
}

func (o *OAuth) Complete(ctx context.Context, loginID, callback string) (Credentials, error) {
	parsed, err := url.Parse(callback)
	expected, _ := url.Parse(DefaultRedirectURI)
	if err != nil || parsed.Scheme != expected.Scheme || parsed.Host != expected.Host || parsed.Path != expected.Path || parsed.Fragment != "" || parsed.User != nil || parsed.Query().Get("state") != loginID || parsed.Query().Get("code") == "" {
		return Credentials{}, errors.New("paste the complete Google callback URL with the matching state and code")
	}
	o.mu.Lock()
	entry, ok := o.pending[loginID]
	delete(o.pending, loginID)
	o.mu.Unlock()
	if !ok || time.Now().After(entry.expires) {
		return Credentials{}, errors.New("Google authorization expired or was already used; start again")
	}
	credentials, err := o.client.token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {parsed.Query().Get("code")}, "code_verifier": {entry.verifier}, "redirect_uri": {DefaultRedirectURI}})
	if err != nil {
		return Credentials{}, err
	}
	return o.client.initialize(ctx, credentials)
}

func (c *Client) token(ctx context.Context, params url.Values) (Credentials, error) {
	if !c.Configured() {
		return Credentials{}, errOAuthNotConfigured
	}
	params.Set("client_id", c.config.ClientID)
	params.Set("client_secret", c.config.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.TokenURL, strings.NewReader(params.Encode()))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return Credentials{}, errors.New("Google token endpoint is unavailable")
	}
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err = decodeResponse(response, &payload); err != nil {
		return Credentials{}, err
	}
	if payload.AccessToken == "" || payload.ExpiresIn <= 0 {
		return Credentials{}, errors.New("Google returned an incomplete token")
	}
	return Credentials{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, ExpiresAt: time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second)}, nil
}

func (c *Client) Refresh(ctx context.Context, current Credentials) (Credentials, error) {
	if current.RefreshToken == "" {
		return Credentials{}, errors.New("Google OAuth refresh token is missing; authorize again")
	}
	refreshed, err := c.token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {current.RefreshToken}})
	if err != nil {
		return Credentials{}, err
	}
	current.AccessToken, current.ExpiresAt = refreshed.AccessToken, refreshed.ExpiresAt
	if refreshed.RefreshToken != "" {
		current.RefreshToken = refreshed.RefreshToken
	}
	return current, nil
}

func (c *Client) initialize(ctx context.Context, credentials Credentials) (Credentials, error) {
	if credentials.RefreshToken == "" {
		return Credentials{}, errors.New("Google did not provide an offline refresh token; authorize again with consent")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.UserInfoURL, nil)
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	response, err := c.config.HTTPClient.Do(req)
	if err != nil {
		return Credentials{}, errors.New("Google identity endpoint is unavailable")
	}
	var identity struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
	}
	if err = decodeResponse(response, &identity); err != nil {
		return Credentials{}, err
	}
	if identity.Subject == "" {
		return Credentials{}, errors.New("Google identity is missing a subject")
	}
	credentials.Subject, credentials.Email = identity.Subject, identity.Email
	credentials.ProjectID, err = c.project(ctx, credentials.AccessToken)
	return credentials, err
}
