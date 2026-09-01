package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

type controlStore struct {
	store.Store
	account          domain.ProviderAccount
	createdKey       domain.APIKey
	capacityErr      bool
	createAccountErr error
	deleteAccountErr error
	deletedAccountID string
	audits           []domain.AuditEvent
	pool             domain.Pool
	status           string
	cooldown         *time.Time
	settings         domain.Settings
}

func (f *controlStore) CreateProviderAccount(_ context.Context, account domain.ProviderAccount) error {
	if f.createAccountErr != nil {
		return f.createAccountErr
	}
	f.account = account
	return nil
}
func (f *controlStore) DeleteProviderAccount(_ context.Context, accountID string) error {
	if f.deleteAccountErr != nil {
		return f.deleteAccountErr
	}
	f.deletedAccountID = accountID
	return nil
}
func (f *controlStore) CreateAPIKeyAndBind(_ context.Context, key domain.APIKey) (string, error) {
	if f.capacityErr {
		return "", store.ErrCapacityExhausted
	}
	f.createdKey = key
	return "account-1", nil
}
func (f *controlStore) Audit(_ context.Context, event domain.AuditEvent) error {
	f.audits = append(f.audits, event)
	return nil
}
func (f *controlStore) CreatePool(_ context.Context, pool domain.Pool) error {
	f.pool = pool
	return nil
}
func (f *controlStore) UpdatePool(_ context.Context, pool domain.Pool) error {
	f.pool = pool
	return nil
}
func (f *controlStore) GetProviderAccount(context.Context, string) (domain.ProviderAccount, error) {
	return f.account, nil
}
func (f *controlStore) UpdateProviderStatus(_ context.Context, _ string, status string, cooldown *time.Time) error {
	f.status = status
	f.cooldown = cooldown
	return nil
}
func (f *controlStore) GetSettings(context.Context) (domain.Settings, error) {
	if f.settings.MaxAPIKeysPerAccount == 0 {
		f.settings.MaxAPIKeysPerAccount = 3
	}
	return f.settings, nil
}
func (f *controlStore) UpdateSettings(_ context.Context, settings domain.Settings) error {
	if f.capacityErr {
		return store.ErrCapacityExhausted
	}
	f.settings = settings
	return nil
}

type controlOAuth struct{ credentials codex.Credentials }

func (f *controlOAuth) Start(string, int) (string, error) {
	return "https://auth.example/authorize", nil
}
func (f *controlOAuth) Exchange(context.Context, string, string) (codex.Credentials, string, int, error) {
	return f.credentials, "Imported account", 3, nil
}
func (f *controlOAuth) Refresh(context.Context, string) (codex.Credentials, error) {
	return f.credentials, nil
}

type controlRefresher struct {
	account domain.ProviderAccount
	err     error
}

func (f *controlRefresher) RefreshAccount(context.Context, string, int) (domain.ProviderAccount, error) {
	return f.account, f.err
}

func TestAdminAuthenticationProtectsControlPlane(t *testing.T) {
	server, _, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`)))
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", login.Code, login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %#v", cookie)
	}
}

func TestSessionEndpointRestoresAuthenticatedAdmin(t *testing.T) {
	server, _, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)

	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/session", nil)
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"authenticated":true`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOAuthCallbackEncryptsCredentialsAndRedirects(t *testing.T) {
	server, st, cipher := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/oauth/callback?state=state&code=code", nil))
	if recorder.Code != http.StatusSeeOther || recorder.Header().Get("Location") != "/#/accounts" {
		t.Fatalf("callback = %d %q", recorder.Code, recorder.Header().Get("Location"))
	}
	if st.account.ID == "" || st.account.MaxAPIKeys != 3 || st.account.Status != domain.AccountActive {
		t.Fatalf("account = %#v", st.account)
	}
	if len(st.account.SubjectHMAC) != 32 {
		t.Fatalf("subject HMAC length = %d", len(st.account.SubjectHMAC))
	}
	plaintext, err := cipher.Decrypt(st.account.CredentialCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(st.account.CredentialCiphertext, []byte("refresh-token")) {
		t.Fatal("credential was saved as plaintext")
	}
	var credentials codex.Credentials
	_ = json.Unmarshal(plaintext, &credentials)
	if credentials.RefreshToken != "refresh-token" {
		t.Fatalf("credentials = %#v", credentials)
	}
}

func TestOAuthCallbackRejectsDuplicateSubject(t *testing.T) {
	server, st, _ := newControlServer(t)
	st.createAccountErr = store.ErrConflict
	mux := http.NewServeMux()
	server.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/oauth/callback?state=state&code=code", nil))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOAuthCallbackRejectsMissingAccountID(t *testing.T) {
	server, st, _ := newControlServer(t)
	server.oauth.(*controlOAuth).credentials.AccountID = ""
	mux := http.NewServeMux()
	server.Register(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/oauth/callback?state=state&code=code", nil))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if st.account.ID != "" {
		t.Fatal("account without subject was stored")
	}
}

func TestCreateAPIKeyReturnsPlaintextOnceAndStoresOnlyHMAC(t *testing.T) {
	server, st, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	cookie := loginCookie(t, mux)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"pool_id":"pool-1","employee_name":"Example Employee"}`))
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &response)
	plain, _ := response["api_key"].(string)
	if !strings.HasPrefix(plain, "sk-subpool-") {
		t.Fatalf("plain key = %q", plain)
	}
	if len(st.createdKey.KeyHMAC) != 32 || st.createdKey.KeyHint != plain[len(plain)-4:] {
		t.Fatalf("stored key = %#v", st.createdKey)
	}
	if bytes.Contains(st.createdKey.KeyHMAC, []byte(plain)) {
		t.Fatal("plaintext key was stored")
	}
}

func TestDeleteProviderAccount(t *testing.T) {
	server, st, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/provider-accounts/account-1", nil)
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || st.deletedAccountID != "account-1" {
		t.Fatalf("status=%d deleted=%q body=%s", recorder.Code, st.deletedAccountID, recorder.Body.String())
	}
	if len(st.audits) != 1 || st.audits[0].Action != "provider_account.delete" {
		t.Fatalf("audits = %#v", st.audits)
	}
}

func TestDeleteProviderAccountRejectsAssignedKeys(t *testing.T) {
	server, st, _ := newControlServer(t)
	st.deleteAccountErr = store.ErrConflict
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/provider-accounts/account-1", nil)
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "remove assigned API keys") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestCreateAPIKeyCapacityError(t *testing.T) {
	server, st, _ := newControlServer(t)
	st.capacityErr = true
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", strings.NewReader(`{"pool_id":"pool-1","employee_name":"Example Employee"}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "subpool_account_capacity_exhausted") {
		t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestGlobalSettingsUpdate(t *testing.T) {
	server, st, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"max_api_keys_per_account":5}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || st.settings.MaxAPIKeysPerAccount != 5 {
		t.Fatalf("status=%d settings=%#v body=%s", recorder.Code, st.settings, recorder.Body.String())
	}
}

func TestGlobalSettingsRejectsCapacityReduction(t *testing.T) {
	server, st, _ := newControlServer(t)
	st.capacityErr = true
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"max_api_keys_per_account":1}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPoolRequiresNonEmptyModelAllowlist(t *testing.T) {
	server, st, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	cookie := loginCookie(t, mux)
	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/api/v1/pools", body: `{"name":"Primary","model_allowlist":[]}`},
		{method: http.MethodPut, path: "/api/v1/pools/pool-1", body: `{"name":"Primary","model_allowlist":[" "]}`},
	} {
		request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s %s status = %d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
	if st.pool.ID != "" {
		t.Fatalf("pool was persisted: %#v", st.pool)
	}
}

func TestManualRefreshClassifiesFailures(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantStatus string
		cooldown   bool
	}{
		{name: "transient", err: errors.New("network unavailable"), wantStatus: domain.AccountCoolingDown, cooldown: true},
		{name: "invalid grant", err: &codex.TokenError{StatusCode: http.StatusBadRequest, Code: "invalid_grant"}, wantStatus: domain.AccountAuthFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, st, _ := newControlServer(t)
			st.account = domain.ProviderAccount{ID: "account-1", CredentialVersion: 1}
			server.refresher.(*controlRefresher).err = test.err
			mux := http.NewServeMux()
			server.Register(mux)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/provider-accounts/account-1/refresh", nil)
			request.AddCookie(loginCookie(t, mux))
			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadGateway || st.status != test.wantStatus || (st.cooldown != nil) != test.cooldown {
				t.Fatalf("response=%d status=%s cooldown=%v", recorder.Code, st.status, st.cooldown)
			}
		})
	}
}

func newControlServer(t *testing.T) (*Server, *controlStore, *credential.Cipher) {
	t.Helper()
	cipher, err := credential.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st := &controlStore{}
	sessions := auth.NewAdminSessions("admin", "secret", time.Hour, false)
	keys := auth.NewAPIKeys(bytes.Repeat([]byte{2}, 32))
	oauth := &controlOAuth{credentials: codex.Credentials{AccessToken: "access-token", RefreshToken: "refresh-token", AccountID: "upstream-account"}}
	sources, err := auth.NewSourceResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, sessions, keys, cipher, oauth, &controlRefresher{}, sources), st, cipher
}
func loginCookie(t *testing.T, mux *http.ServeMux) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("login failed: %s", recorder.Body.String())
	}
	return recorder.Result().Cookies()[0]
}
