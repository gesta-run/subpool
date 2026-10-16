package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/bailian"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type quotaConfigStore struct {
	controlStore
	saved      bool
	quotaError string
}

func (s *quotaConfigStore) UpdateProviderCredentialCAS(_ context.Context, _ string, old int, raw []byte, next int) (bool, error) {
	if s.account.CredentialVersion != old {
		return false, nil
	}
	s.account.CredentialCiphertext, s.account.CredentialVersion, s.saved = raw, next, true
	return true, nil
}
func (s *quotaConfigStore) SetProviderQuotaError(_ context.Context, _, code string, _ time.Time) error {
	s.quotaError = code
	return nil
}

type quotaConfigUsage struct{ err error }

func (u quotaConfigUsage) Usage(context.Context, bailian.Credentials) (bailian.Snapshot, error) {
	return bailian.Snapshot{PlanType: bailian.PlanType, Weekly: &bailian.Window{RemainingPercent: 75}}, u.err
}

func TestBrowserQuotaTokenIsEncryptedAndPreservesInference(t *testing.T) {
	cipher, _ := credential.New(make([]byte, 32))
	raw := []byte(`{"base_url":"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1","api_key":"illustrative-key"}`)
	encrypted, _ := cipher.Encrypt(raw)
	st := &quotaConfigStore{controlStore: controlStore{account: domain.ProviderAccount{ID: "account", Provider: domain.ProviderOpenAICompatible, CredentialVersion: 1, CredentialCiphertext: encrypted}}}
	s := &Server{store: st, cipher: cipher, bailianQuota: quotaConfigUsage{}}
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"access_token":" illustrative-token ","console_switch_agent":123}`))
	r.SetPathValue("id", "account")
	w := httptest.NewRecorder()
	s.configureBailianQuota(w, r)
	if w.Code != 200 || !st.saved || strings.Contains(w.Body.String(), "illustrative-token") || strings.Contains(string(st.account.CredentialCiphertext), "illustrative-token") {
		t.Fatal("browser token was rejected or leaked")
	}
	plaintext, _ := cipher.Decrypt(st.account.CredentialCiphertext)
	var saved openaicompat.Credentials
	json.Unmarshal(plaintext, &saved)
	if saved.APIKey != "illustrative-key" || saved.BailianQuota == nil || saved.BailianQuota.AccessToken != "illustrative-token" || saved.BailianQuota.ConsoleSwitchAgent != 123 {
		t.Fatal("inference or delegated console credentials changed")
	}
}

func TestBailianQuotaCredentialsStayEncryptedAndPreserveInference(t *testing.T) {
	cipher, _ := credential.New(make([]byte, 32))
	upstream := openaicompat.Credentials{BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "illustrative-inference-key"}
	raw, _ := json.Marshal(upstream)
	encrypted, _ := cipher.Encrypt(raw)
	st := &quotaConfigStore{controlStore: controlStore{account: domain.ProviderAccount{ID: "account", Provider: domain.ProviderOpenAICompatible, CredentialVersion: 1, CredentialCiphertext: encrypted, Status: domain.AccountActive}}}
	s := &Server{store: st, cipher: cipher, bailianQuota: quotaConfigUsage{}}
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"access_key_id":"illustrative-id","access_key_secret":"illustrative-secret"}`))
	r.SetPathValue("id", "account")
	w := httptest.NewRecorder()
	s.configureBailianQuota(w, r)
	if w.Code != 200 || !st.saved {
		t.Fatalf("save failed: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "illustrative-secret") || strings.Contains(string(st.account.CredentialCiphertext), "illustrative-secret") {
		t.Fatal("quota secret leaked")
	}
	plaintext, _ := cipher.Decrypt(st.account.CredentialCiphertext)
	var saved openaicompat.Credentials
	json.Unmarshal(plaintext, &saved)
	if saved.APIKey != upstream.APIKey || saved.BaseURL != upstream.BaseURL || saved.BailianQuota == nil || st.account.Status != domain.AccountActive {
		t.Fatal("inference credentials changed")
	}
	r = httptest.NewRequest(http.MethodDelete, "/", nil)
	r.SetPathValue("id", "account")
	w = httptest.NewRecorder()
	s.configureBailianQuota(w, r)
	plaintext, _ = cipher.Decrypt(st.account.CredentialCiphertext)
	saved = openaicompat.Credentials{}
	json.Unmarshal(plaintext, &saved)
	if w.Code != 200 || saved.BailianQuota != nil || saved.APIKey != upstream.APIKey || st.quotaError != "quota_authorization_required" {
		t.Fatal("disconnect changed inference or retained credentials")
	}
}

func TestBailianQuotaRejectsUnauthenticatedConfiguration(t *testing.T) {
	mux := http.NewServeMux()
	s := &Server{}
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/provider-accounts/account/bailian-quota", strings.NewReader(`{}`)))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated write accepted: %d", w.Code)
	}
}

func TestRejectedBailianAuthorizationIsNotStored(t *testing.T) {
	cipher, _ := credential.New(make([]byte, 32))
	raw := []byte(`{"base_url":"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1","api_key":"illustrative-key"}`)
	encrypted, _ := cipher.Encrypt(raw)
	st := &quotaConfigStore{controlStore: controlStore{account: domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible, CredentialCiphertext: encrypted}}}
	s := &Server{store: st, cipher: cipher, bailianQuota: quotaConfigUsage{err: bailian.ErrPermissionDenied}}
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"access_key_id":"illustrative-id","access_key_secret":"illustrative-secret"}`))
	w := httptest.NewRecorder()
	s.configureBailianQuota(w, r)
	if w.Code != http.StatusBadGateway || st.saved {
		t.Fatal("rejected authorization was stored")
	}
}

func TestAccountListingDiscoversQuotaWithoutListingCredentials(t *testing.T) {
	cipher, _ := credential.New(make([]byte, 32))
	raw := []byte(`{"base_url":"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1","api_key":"illustrative-inference-key","bailian_quota":{"access_key_id":"illustrative-id","access_key_secret":"illustrative-secret"}}`)
	encrypted, _ := cipher.Encrypt(raw)
	listed := domain.ProviderAccount{ID: "account", Provider: domain.ProviderOpenAICompatible}
	stored := listed
	stored.CredentialCiphertext = encrypted
	st := &controlStore{accounts: []domain.ProviderAccount{listed}, accountByID: map[string]domain.ProviderAccount{"account": stored}}
	s := &Server{store: st, cipher: cipher}
	w := httptest.NewRecorder()
	s.listProviderAccounts(w, httptest.NewRequest(http.MethodGet, "/", nil))
	var response struct {
		Data []domain.ProviderAccount `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(response.Data) != 1 || response.Data[0].QuotaProvider != bailian.PlanType || !response.Data[0].QuotaAuthorized || response.Data[0].QuotaAuthorizationMethod != "ram" || response.Data[0].QuotaAccessKeyIDHint != "****e-id" {
		t.Fatalf("quota discovery failed: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "illustrative-inference-key") || strings.Contains(w.Body.String(), "illustrative-id") || strings.Contains(w.Body.String(), "illustrative-secret") || len(response.Data[0].CredentialCiphertext) > 0 {
		t.Fatal("listing leaked credentials")
	}
}

func TestBailianQuotaDescriptionIncludesOnlySafeAuthorizationMetadata(t *testing.T) {
	for _, tt := range []struct {
		name        string
		credentials *bailian.Credentials
		method      string
		hint        string
	}{
		{name: "not configured"},
		{name: "browser token", credentials: &bailian.Credentials{AccessToken: "illustrative-token"}, method: "browser"},
		{name: "RAM key", credentials: &bailian.Credentials{AccessKeyID: "illustrative-key-1234", AccessKeySecret: "illustrative-secret"}, method: "ram", hint: "****1234"},
		{name: "short key", credentials: &bailian.Credentials{AccessKeyID: "demo", AccessKeySecret: "illustrative-secret"}, method: "ram", hint: "****"},
		{name: "eight character key", credentials: &bailian.Credentials{AccessKeyID: "demo1234", AccessKeySecret: "illustrative-secret"}, method: "ram", hint: "****"},
		{name: "token takes precedence", credentials: &bailian.Credentials{AccessToken: "illustrative-token", AccessKeyID: "illustrative-key-1234", AccessKeySecret: "illustrative-secret"}, method: "browser"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cipher, err := credential.New(make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(openaicompat.Credentials{BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "illustrative-inference-key", BailianQuota: tt.credentials})
			if err != nil {
				t.Fatal(err)
			}
			encrypted, err := cipher.Encrypt(raw)
			if err != nil {
				t.Fatal(err)
			}
			account := domain.ProviderAccount{ID: "illustrative-account", Provider: domain.ProviderOpenAICompatible, QuotaAuthorizationMethod: "stale", QuotaAccessKeyIDHint: "stale"}
			stored := account
			stored.CredentialCiphertext = encrypted
			s := &Server{store: &controlStore{account: stored}, cipher: cipher}
			s.describeBailianQuota(context.Background(), &account)
			if account.QuotaAuthorized != (tt.credentials != nil) || account.QuotaAuthorizationMethod != tt.method || account.QuotaAccessKeyIDHint != tt.hint {
				t.Fatalf("unexpected authorization metadata: %+v", account)
			}
			response, err := json.Marshal(account)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"illustrative-inference-key", "illustrative-token", "illustrative-secret"} {
				if strings.Contains(string(response), value) {
					t.Fatal("authorization metadata leaked a credential")
				}
			}
			if tt.credentials != nil && tt.credentials.AccessKeyID != "" && strings.Contains(string(response), tt.credentials.AccessKeyID) {
				t.Fatal("authorization metadata leaked the complete AccessKey ID")
			}
			if tt.credentials == nil && (strings.Contains(string(response), "quota_authorization_method") || strings.Contains(string(response), "quota_access_key_id_hint")) {
				t.Fatal("unconfigured authorization included credential metadata")
			}
		})
	}
}

func TestRejectedBailianReplacementPreservesSavedAuthorization(t *testing.T) {
	cipher, err := credential.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"base_url":"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1","api_key":"illustrative-inference-key","bailian_quota":{"access_key_id":"illustrative-old-key","access_key_secret":"illustrative-old-secret"}}`)
	encrypted, err := cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	st := &quotaConfigStore{controlStore: controlStore{account: domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible, CredentialVersion: 2, CredentialCiphertext: encrypted}}}
	s := &Server{store: st, cipher: cipher, bailianQuota: quotaConfigUsage{err: bailian.ErrPermissionDenied}}
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"access_key_id":"illustrative-new-key","access_key_secret":"illustrative-new-secret"}`))
	w := httptest.NewRecorder()
	s.configureBailianQuota(w, r)
	if w.Code != http.StatusBadGateway || st.saved || string(st.account.CredentialCiphertext) != string(encrypted) || st.account.CredentialVersion != 2 {
		t.Fatal("rejected replacement changed the saved authorization")
	}
}
