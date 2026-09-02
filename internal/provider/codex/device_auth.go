package codex

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	deviceLoginTTL        = 10 * time.Minute
	deviceHandshakeTTL    = 15 * time.Second
	maxPendingDeviceLogin = 8
)

type DeviceAuthorization struct {
	LoginID         string    `json:"login_id"`
	UserCode        string    `json:"user_code"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type DeviceAuthorizationResult struct {
	Credentials Credentials
	Err         error
}

type pendingDeviceAuthorization struct {
	session *appServerSession
	result  chan DeviceAuthorizationResult
}

type DeviceAuth struct {
	executable string
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	pending    map[string]*pendingDeviceAuthorization
	starting   int
	now        func() time.Time
}

func NewDeviceAuth() *DeviceAuth {
	ctx, cancel := context.WithCancel(context.Background())
	return &DeviceAuth{
		executable: defaultCodexExecutable,
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[string]*pendingDeviceAuthorization),
		now:        time.Now,
	}
}

func (d *DeviceAuth) Start(ctx context.Context) (DeviceAuthorization, <-chan DeviceAuthorizationResult, error) {
	if err := ctx.Err(); err != nil {
		return DeviceAuthorization{}, nil, err
	}
	if err := d.ctx.Err(); err != nil {
		return DeviceAuthorization{}, nil, fmt.Errorf("Codex device authorization is shutting down")
	}
	d.mu.Lock()
	if len(d.pending)+d.starting >= maxPendingDeviceLogin {
		d.mu.Unlock()
		return DeviceAuthorization{}, nil, fmt.Errorf("too many Codex authorizations are already pending")
	}
	d.starting++
	d.mu.Unlock()
	reserved := true
	defer func() {
		if reserved {
			d.mu.Lock()
			d.starting--
			d.mu.Unlock()
		}
	}()

	session, err := launchAppServerProcess(d.ctx, d.executable, Credentials{})
	if err != nil {
		return DeviceAuthorization{}, nil, err
	}
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, deviceHandshakeTTL)
	defer cancelHandshake()
	if err = initializeAppServer(handshakeCtx, session); err != nil {
		session.close()
		return DeviceAuthorization{}, nil, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	var response struct {
		Type            string `json:"type"`
		LoginID         string `json:"loginId"`
		UserCode        string `json:"userCode"`
		VerificationURL string `json:"verificationUrl"`
	}
	err = session.callContext(handshakeCtx, 2, "account/login/start", map[string]any{"type": "chatgptDeviceCode"}, &response)
	if err != nil {
		session.close()
		return DeviceAuthorization{}, nil, err
	}
	if response.Type != "chatgptDeviceCode" || strings.TrimSpace(response.LoginID) == "" || strings.TrimSpace(response.UserCode) == "" || !validVerificationURL(response.VerificationURL) {
		session.close()
		return DeviceAuthorization{}, nil, fmt.Errorf("Codex returned an invalid device authorization response")
	}
	publicID, err := randomURLString(24)
	if err != nil {
		session.close()
		return DeviceAuthorization{}, nil, err
	}
	result := make(chan DeviceAuthorizationResult, 1)
	pending := &pendingDeviceAuthorization{session: session, result: result}
	d.mu.Lock()
	d.starting--
	reserved = false
	d.pending[publicID] = pending
	d.mu.Unlock()

	authorization := DeviceAuthorization{
		LoginID:         publicID,
		UserCode:        response.UserCode,
		VerificationURL: response.VerificationURL,
		ExpiresAt:       d.now().Add(deviceLoginTTL),
	}
	go d.wait(publicID, response.LoginID, pending)
	return authorization, result, nil
}

func (d *DeviceAuth) Cancel(loginID string) {
	d.mu.Lock()
	pending, ok := d.pending[loginID]
	if ok {
		delete(d.pending, loginID)
	}
	d.mu.Unlock()
	if ok {
		pending.session.close()
	}
}

func (d *DeviceAuth) Close() {
	d.cancel()
	d.mu.Lock()
	pending := make([]*pendingDeviceAuthorization, 0, len(d.pending))
	for loginID, attempt := range d.pending {
		delete(d.pending, loginID)
		pending = append(pending, attempt)
	}
	d.mu.Unlock()
	for _, attempt := range pending {
		attempt.session.close()
	}
}

func (d *DeviceAuth) wait(publicID, upstreamLoginID string, pending *pendingDeviceAuthorization) {
	timer := time.AfterFunc(deviceLoginTTL, pending.session.close)
	err := pending.session.waitForDeviceLogin(upstreamLoginID)
	if !timer.Stop() && err == nil {
		err = context.DeadlineExceeded
	}
	var credentials Credentials
	if err == nil {
		credentials, err = readDeviceCredentials(pending.session.configDir)
	}
	pending.session.close()

	d.mu.Lock()
	current, ok := d.pending[publicID]
	if ok && current == pending {
		delete(d.pending, publicID)
	}
	d.mu.Unlock()
	if ok {
		pending.result <- DeviceAuthorizationResult{Credentials: credentials, Err: err}
	}
	close(pending.result)
}

func (s *appServerSession) waitForDeviceLogin(loginID string) error {
	for s.scanner.Scan() {
		var message struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(s.scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("decode Codex app-server notification: %w", err)
		}
		if message.Method != "account/login/completed" {
			continue
		}
		var completed struct {
			LoginID string  `json:"loginId"`
			Success bool    `json:"success"`
			Error   *string `json:"error"`
		}
		if err := json.Unmarshal(message.Params, &completed); err != nil {
			return fmt.Errorf("decode Codex device authorization result: %w", err)
		}
		if completed.LoginID != "" && completed.LoginID != loginID {
			continue
		}
		if completed.Success {
			return nil
		}
		if completed.Error != nil && strings.TrimSpace(*completed.Error) != "" {
			return fmt.Errorf("Codex device authorization failed: %s", truncateMessage(*completed.Error))
		}
		return fmt.Errorf("Codex device authorization failed")
	}
	if err := s.scanner.Err(); err != nil {
		return fmt.Errorf("read Codex app-server notification: %w", err)
	}
	if strings.TrimSpace(s.stderr.String()) != "" {
		return fmt.Errorf("Codex app-server stopped during device authorization: %s", truncateMessage(s.stderr.String()))
	}
	return fmt.Errorf("Codex app-server stopped during device authorization")
}

func readDeviceCredentials(configDir string) (Credentials, error) {
	path := filepath.Join(configDir, "auth.json")
	var raw []byte
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err = os.ReadFile(path)
		if err == nil || !errors.Is(err, os.ErrNotExist) || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		return Credentials{}, fmt.Errorf("read Codex device credentials: %w", err)
	}
	var authFile struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			IDToken      string `json:"id_token"`
			AccountID    string `json:"account_id"`
		} `json:"tokens"`
	}
	if err = json.Unmarshal(raw, &authFile); err != nil {
		return Credentials{}, fmt.Errorf("decode Codex device credentials: %w", err)
	}
	credentials := Credentials{
		AccessToken:  strings.TrimSpace(authFile.Tokens.AccessToken),
		RefreshToken: strings.TrimSpace(authFile.Tokens.RefreshToken),
		IDToken:      strings.TrimSpace(authFile.Tokens.IDToken),
		AccountID:    strings.TrimSpace(authFile.Tokens.AccountID),
	}
	identityAccountID, email := parseIdentity(credentials.IDToken)
	if credentials.AccountID == "" {
		credentials.AccountID = identityAccountID
	}
	credentials.Email = email
	credentials.ExpiresAt = tokenExpiry(credentials.AccessToken)
	if credentials.AccessToken == "" || credentials.RefreshToken == "" || credentials.AccountID == "" {
		return Credentials{}, fmt.Errorf("Codex device credentials are incomplete")
	}
	return credentials, nil
}

func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.ExpiresAt <= 0 {
		return time.Time{}
	}
	return time.Unix(claims.ExpiresAt, 0).UTC()
}

func validVerificationURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

func truncateMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 300 {
		return value[:300]
	}
	return value
}

func randomURLString(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate authorization secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
