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
