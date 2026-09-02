package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
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
	accounts         []domain.ProviderAccount
	accountByID      map[string]domain.ProviderAccount
	membership       domain.PoolAccount
	resetSnapshot    []byte
	resetCheckedAt   *time.Time
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
		return "", store.ErrNoEligibleAccount
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
func (f *controlStore) GetProviderAccount(_ context.Context, id string) (domain.ProviderAccount, error) {
	if account, ok := f.accountByID[id]; ok {
		return account, nil
	}
	account := f.account
	if account.ID == "" {
		account.ID = id
	}
	if account.Provider == "" {
		account.Provider = domain.ProviderCodex
	}
	return account, nil
}
func (f *controlStore) AddPoolAccount(_ context.Context, membership domain.PoolAccount) error {
	f.membership = membership
	return nil
}
func (f *controlStore) ListProviderAccounts(context.Context) ([]domain.ProviderAccount, error) {
	return f.accounts, nil
}
func (f *controlStore) UpdateProviderStatus(_ context.Context, _ string, status string, cooldown *time.Time) error {
	f.status = status
	f.cooldown = cooldown
	return nil
}
func (f *controlStore) GetSettings(context.Context) (domain.Settings, error) {
	if f.settings.MaxAPIKeysPerAccount == 0 {
		f.settings.MaxAPIKeysPerAccount = 2
	}
	return f.settings, nil
}
func (f *controlStore) UpdateSettings(_ context.Context, settings domain.Settings) error {
	f.settings = settings
	return nil
}
func (f *controlStore) GetProviderResetCredits(context.Context, string) ([]byte, *time.Time, error) {
	return f.resetSnapshot, f.resetCheckedAt, nil
}
func (f *controlStore) SetProviderResetCredits(_ context.Context, _ string, snapshot []byte, checkedAt time.Time) error {
	f.resetSnapshot = append([]byte(nil), snapshot...)
	f.resetCheckedAt = &checkedAt
	return nil
}
func (f *controlStore) ClaimProviderResetCreditRefresh(context.Context, string, time.Time, time.Time) (bool, error) {
	return true, nil
}
func (f *controlStore) ReleaseProviderResetCreditRefresh(context.Context, string) error { return nil }

type controlOAuth struct{ credentials codex.Credentials }

func (f *controlOAuth) Start(string) (string, error) {
	return "https://auth.example/authorize", nil
}
func (f *controlOAuth) Exchange(context.Context, string, string) (codex.Credentials, string, error) {
	return f.credentials, "Imported account", nil
}
func (f *controlOAuth) Refresh(context.Context, string) (codex.Credentials, error) {
	return f.credentials, nil
}

type controlRefresher struct {
	account domain.ProviderAccount
	err     error
}

type controlCodexModels struct {
	credentials codex.Credentials
	models      []codex.Model
	err         error
}

func (f *controlCodexModels) ListModels(_ context.Context, credentials codex.Credentials) ([]codex.Model, error) {
	f.credentials = credentials
	return f.models, f.err
}

type controlCompatibleModels struct {
	credentials openaicompat.Credentials
	response    *http.Response
	err         error
}

func (f *controlCompatibleModels) Models(_ context.Context, credentials openaicompat.Credentials) (*http.Response, error) {
	f.credentials = credentials
	return f.response, f.err
}

type controlResetCredits struct {
	credits       *codex.ResetCreditsSummary
	consumeResult codex.ConsumeResetCreditResult
	err           error
	credentials   codex.Credentials
	creditID      string
	idempotency   string
	readCalls     int
}

func (f *controlResetCredits) ReadResetCredits(_ context.Context, credentials codex.Credentials) (*codex.ResetCreditsSummary, error) {
	f.readCalls++
	f.credentials = credentials
	return f.credits, f.err
}

func (f *controlResetCredits) ConsumeResetCredit(_ context.Context, credentials codex.Credentials, creditID, idempotency string) (codex.ConsumeResetCreditResult, error) {
	f.credentials = credentials
	f.creditID = creditID
	f.idempotency = idempotency
	return f.consumeResult, f.err
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

func TestCreateOpenAICompatibleAccountEncryptsCredentials(t *testing.T) {
	server, st, cipher := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/provider-accounts", strings.NewReader(`{"provider":"openai_compatible","display_name":"Production endpoint","base_url":"https://api.example.com/v1/","api_key":"sk-test-placeholder"}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if st.account.Provider != domain.ProviderOpenAICompatible || st.account.CredentialType != domain.CredentialAPIKey || st.account.DisplayName != "Production endpoint" {
		t.Fatalf("account = %#v", st.account)
	}
	if strings.Contains(recorder.Body.String(), "sk-test-placeholder") || strings.Contains(recorder.Body.String(), "api.example.com") {
		t.Fatalf("credentials leaked in response: %s", recorder.Body.String())
	}
	plaintext, err := cipher.Decrypt(st.account.CredentialCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]string
	if err = json.Unmarshal(plaintext, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["base_url"] != "https://api.example.com/v1" || stored["api_key"] != "sk-test-placeholder" {
		t.Fatalf("stored credentials = %#v", stored)
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
	if st.account.ID == "" || st.account.Status != domain.AccountActive {
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

func TestListProviderAccountsUsesCachedSubscriptionUsage(t *testing.T) {
	server, st, _ := newControlServer(t)
	st.accounts = []domain.ProviderAccount{{
		ID: "account-1", CredentialType: domain.CredentialSubscription, DisplayName: "Primary", Email: "employee@example.com",
		QuotaSnapshot: json.RawMessage(`{"plan_type":"plus","weekly":{"used_percent":40,"remaining_percent":60,"window_seconds":604800,"reset_at":1800500000}}`),
	}}
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts", nil)
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"remaining_percent":60`) || !strings.Contains(recorder.Body.String(), `"email":"employee@example.com"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestListProviderModelsUsesSelectedAccountCredentials(t *testing.T) {
	t.Run("Codex subscription", func(t *testing.T) {
		server, st, cipher := newControlServer(t)
		raw, _ := json.Marshal(codex.Credentials{AccessToken: "access-token", AccountID: "account-subject"})
		encrypted, err := cipher.Encrypt(raw)
		if err != nil {
			t.Fatal(err)
		}
		st.account = domain.ProviderAccount{ID: "account-1", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, CredentialCiphertext: encrypted, CredentialVersion: 1}
		codexModels := &controlCodexModels{models: []codex.Model{
			{ID: "catalog-beta", Model: "model-beta", DisplayName: "Model Beta", Hidden: true},
			{ID: "catalog-alpha", Model: "model-alpha", DisplayName: "Model Alpha", Description: "General-purpose model", IsDefault: true, InputModalities: []string{"text", "image"}, SupportedReasoningEfforts: []codex.ReasoningEffortOption{{ReasoningEffort: "high"}}},
		}}
		server.WithModelProviders(codexModels, nil)
		mux := http.NewServeMux()
		server.Register(mux)
		request := httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/account-1/models", nil)
		request.AddCookie(loginCookie(t, mux))
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"model-alpha"`) || strings.Contains(recorder.Body.String(), "model-beta") {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if codexModels.credentials.AccountID != "account-subject" || !strings.Contains(recorder.Body.String(), `"reasoning_efforts":["high"]`) {
			t.Fatalf("credentials=%#v body=%s", codexModels.credentials, recorder.Body.String())
		}
	})

	t.Run("OpenAI-compatible API", func(t *testing.T) {
		server, st, cipher := newControlServer(t)
		raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.example.test/v1", APIKey: "sk-upstream-placeholder"})
		encrypted, err := cipher.Encrypt(raw)
		if err != nil {
			t.Fatal(err)
		}
		st.account = domain.ProviderAccount{ID: "account-2", Provider: domain.ProviderOpenAICompatible, CredentialType: domain.CredentialAPIKey, CredentialCiphertext: encrypted}
		compatibleModels := &controlCompatibleModels{response: &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"model-gamma"},{"id":"model-alpha"}]}`)),
		}}
		server.WithModelProviders(nil, compatibleModels)
		mux := http.NewServeMux()
		server.Register(mux)
		request := httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/account-2/models", nil)
		request.AddCookie(loginCookie(t, mux))
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"model-alpha"`) || !strings.Contains(recorder.Body.String(), `"id":"model-gamma"`) {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if compatibleModels.credentials.APIKey != "sk-upstream-placeholder" || strings.Contains(recorder.Body.String(), "sk-upstream-placeholder") {
			t.Fatalf("credentials=%#v body=%s", compatibleModels.credentials, recorder.Body.String())
		}
	})
}

func TestCodexResetCreditsCanBeReadAndConsumed(t *testing.T) {
	server, st, cipher := newControlServer(t)
	credentials, _ := json.Marshal(codex.Credentials{AccessToken: "test-access", AccountID: "test-upstream-account", ExpiresAt: time.Now().Add(time.Hour)})
	encrypted, err := cipher.Encrypt(credentials)
	if err != nil {
		t.Fatal(err)
	}
	st.account = domain.ProviderAccount{
		ID: "account-1", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription,
		CredentialCiphertext: encrypted, CredentialVersion: 1, Status: domain.AccountExhausted,
	}
	resets := server.resets.(*controlResetCredits)
	expiresAt := int64(1800500000)
	resets.credits = &codex.ResetCreditsSummary{AvailableCount: 2, Credits: []codex.ResetCredit{{ID: "credit-1", Status: "available", ExpiresAt: &expiresAt}}}
	resets.consumeResult = codex.ConsumeResetCreditResult{Outcome: "reset", ResetCredits: &codex.ResetCreditsSummary{AvailableCount: 1}}
	mux := http.NewServeMux()
	server.Register(mux)
	cookie := loginCookie(t, mux)

	readRequest := httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/account-1/reset-credits", nil)
	readRequest.AddCookie(cookie)
	readResponse := httptest.NewRecorder()
	mux.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK || !strings.Contains(readResponse.Body.String(), `"available_count":2`) || !strings.Contains(readResponse.Body.String(), `"expires_at":1800500000`) {
		t.Fatalf("read status=%d body=%s", readResponse.Code, readResponse.Body.String())
	}
	cachedResponse := httptest.NewRecorder()
	cachedRequest := httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/account-1/reset-credits", nil)
	cachedRequest.AddCookie(cookie)
	mux.ServeHTTP(cachedResponse, cachedRequest)
	if cachedResponse.Code != http.StatusOK || resets.readCalls != 1 {
		t.Fatalf("cached status=%d read calls=%d body=%s", cachedResponse.Code, resets.readCalls, cachedResponse.Body.String())
	}
	refreshResponse := httptest.NewRecorder()
	refreshRequest := httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/account-1/reset-credits?refresh=true", nil)
	refreshRequest.AddCookie(cookie)
	mux.ServeHTTP(refreshResponse, refreshRequest)
	if refreshResponse.Code != http.StatusOK || resets.readCalls != 2 {
		t.Fatalf("refresh status=%d read calls=%d body=%s", refreshResponse.Code, resets.readCalls, refreshResponse.Body.String())
	}

	consumeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/provider-accounts/account-1/reset-credits/consume", strings.NewReader(`{"credit_id":"credit-1","idempotency_key":"00000000-0000-4000-8000-000000000001"}`))
	consumeRequest.AddCookie(cookie)
	consumeResponse := httptest.NewRecorder()
	mux.ServeHTTP(consumeResponse, consumeRequest)
	if consumeResponse.Code != http.StatusOK || !strings.Contains(consumeResponse.Body.String(), `"outcome":"reset"`) {
		t.Fatalf("consume status=%d body=%s", consumeResponse.Code, consumeResponse.Body.String())
	}
	if resets.credentials.AccountID != "test-upstream-account" || resets.creditID != "credit-1" || resets.idempotency == "" {
		t.Fatalf("reset request = %#v", resets)
	}
	if st.status != domain.AccountActive || st.cooldown != nil {
		t.Fatalf("status=%s cooldown=%v", st.status, st.cooldown)
	}
	if len(st.audits) != 1 || st.audits[0].Action != "provider_account.reset_credit.consume" || st.audits[0].Result != "success" {
		t.Fatalf("audits = %#v", st.audits)
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
	if !strings.HasPrefix(plain, "sk-") {
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
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "subpool_no_eligible_account") {
		t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestGlobalSettingsUpdate(t *testing.T) {
	server, st, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPut, "/api/v1/settings", strings.NewReader(`{"max_api_keys_per_account":4}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || st.settings.MaxAPIKeysPerAccount != 4 {
		t.Fatalf("status=%d settings=%#v body=%s", recorder.Code, st.settings, recorder.Body.String())
	}
}

func TestPoolRequiresAccountsAndAllowsAnyModel(t *testing.T) {
	server, st, _ := newControlServer(t)
	st.accountByID = map[string]domain.ProviderAccount{
		"account-1": {ID: "account-1", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription},
		"account-2": {ID: "account-2", Provider: domain.ProviderOpenAICompatible, CredentialType: domain.CredentialAPIKey},
	}
	mux := http.NewServeMux()
	server.Register(mux)
	cookie := loginCookie(t, mux)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/pools", strings.NewReader(`{"name":"Primary","provider_account_ids":[]}`))
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty accounts status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if st.pool.ID != "" {
		t.Fatalf("pool was persisted: %#v", st.pool)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/pools", strings.NewReader(`{"name":"Primary","provider_account_ids":["account-1","account-2","account-1"]}`))
	request.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if st.pool.Provider != domain.ProviderMixed || len(st.pool.Accounts) != 2 {
		t.Fatalf("pool = %#v", st.pool)
	}
	if st.pool.Accounts[0].ProviderAccountID != "account-1" || st.pool.Accounts[1].ProviderAccountID != "account-2" {
		t.Fatalf("memberships = %#v", st.pool.Accounts)
	}
	if st.pool.Accounts[0].Priority != 0 || st.pool.Accounts[1].Priority != 100 {
		t.Fatalf("membership priorities = %#v", st.pool.Accounts)
	}

	request = httptest.NewRequest(http.MethodPut, "/api/v1/pools/"+st.pool.ID, strings.NewReader(`{"name":"Renamed"}`))
	request.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || st.pool.Name != "Renamed" {
		t.Fatalf("update status = %d pool=%#v body=%s", recorder.Code, st.pool, recorder.Body.String())
	}
}

func TestAddingAPIAccountDefaultsToFallbackPriority(t *testing.T) {
	server, st, _ := newControlServer(t)
	st.accountByID = map[string]domain.ProviderAccount{
		"account-api": {ID: "account-api", Provider: domain.ProviderOpenAICompatible, CredentialType: domain.CredentialAPIKey},
	}
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pools/pool-1/accounts", strings.NewReader(`{"provider_account_id":"account-api"}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()

	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || st.membership.ProviderAccountID != "account-api" || st.membership.Priority != 100 {
		t.Fatalf("status=%d membership=%#v body=%s", recorder.Code, st.membership, recorder.Body.String())
	}
}

func TestAddingPoolAccountRejectsInvalidWeight(t *testing.T) {
	server, st, _ := newControlServer(t)
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/pools/pool-1/accounts", strings.NewReader(`{"provider_account_id":"account-1","weight":101}`))
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()

	mux.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if st.membership.ProviderAccountID != "" {
		t.Fatalf("membership was persisted: %#v", st.membership)
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

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"Primary"} {"name":"Secondary"}`))
	recorder := httptest.NewRecorder()
	var target struct {
		Name string `json:"name"`
	}

	if decode(recorder, request, &target) {
		t.Fatal("decode accepted multiple JSON values")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func newControlServer(t *testing.T) (*Server, *controlStore, *credential.Cipher) {
	t.Helper()
	cipher, err := credential.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st := &controlStore{}
	sessions := auth.NewAdminSessions("admin", "secret", time.Hour, false, bytes.Repeat([]byte{3}, 32))
	keys := auth.NewAPIKeys(bytes.Repeat([]byte{2}, 32))
	oauth := &controlOAuth{credentials: codex.Credentials{AccessToken: "access-token", RefreshToken: "refresh-token", AccountID: "upstream-account"}}
	sources, err := auth.NewSourceResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, sessions, keys, cipher, oauth, &controlRefresher{}, sources).WithResetCredits(&controlResetCredits{}), st, cipher
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
