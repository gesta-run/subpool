package control

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type billingConfigProbe struct{ err error }

func (p billingConfigProbe) VerifyAccount(context.Context, string, string) error { return p.err }
func (p billingConfigProbe) Usage(context.Context, openaibilling.Credentials) (openaibilling.Snapshot, error) {
	return openaibilling.Snapshot{PlanType: openaibilling.PlanType}, nil
}
func TestBillingCredentialsEncryptedAndRejectedOrganizationNotSaved(t *testing.T) {
	for _, reject := range []bool{false, true} {
		cipher, _ := credential.New(make([]byte, 32))
		raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.openai.com/v1", APIKey: "illustrative-model-key"})
		encrypted, _ := cipher.Encrypt(raw)
		st := &quotaConfigStore{controlStore: controlStore{account: domain.ProviderAccount{ID: "example", Provider: domain.ProviderOpenAICompatible, CredentialVersion: 1, CredentialCiphertext: encrypted}}}
		probe := billingConfigProbe{}
		if reject {
			probe.err = errors.New("organization mismatch")
		}
		s := &Server{store: st, cipher: cipher, openaiBalance: probe}
		r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"session_key":"example-session","organization_id":"org-example"}`))
		r.SetPathValue("id", "example")
		w := httptest.NewRecorder()
		s.configureOpenAIBalance(w, r)
		if reject {
			if w.Code != 502 || st.saved {
				t.Fatal("rejected billing organization saved")
			}
			continue
		}
		if w.Code != 200 || !st.saved || strings.Contains(w.Body.String(), "example-session") || strings.Contains(string(st.account.CredentialCiphertext), "example-session") {
			t.Fatal("billing auth rejected or exposed")
		}
		plain, _ := cipher.Decrypt(st.account.CredentialCiphertext)
		var stored openaicompat.Credentials
		json.Unmarshal(plain, &stored)
		if stored.APIKey != "illustrative-model-key" || stored.OpenAIBilling == nil || stored.OpenAIBilling.SessionKey != "example-session" {
			t.Fatal("incorrect credential persistence")
		}
		r = httptest.NewRequest(http.MethodDelete, "/", nil)
		r.SetPathValue("id", "example")
		s.configureOpenAIBalance(httptest.NewRecorder(), r)
		plain, _ = cipher.Decrypt(st.account.CredentialCiphertext)
		stored = openaicompat.Credentials{}
		json.Unmarshal(plain, &stored)
		if stored.OpenAIBilling != nil || stored.APIKey != "illustrative-model-key" {
			t.Fatal("disconnect retained session or changed model credentials")
		}
	}
}
func TestCostsCredentialsEncryptedWithoutChangingInference(t *testing.T) {
	cipher, _ := credential.New(make([]byte, 32))
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.openai.com/v1", APIKey: "illustrative-model-key"})
	encrypted, _ := cipher.Encrypt(raw)
	st := &quotaConfigStore{controlStore: controlStore{account: domain.ProviderAccount{ID: "example", Provider: domain.ProviderOpenAICompatible, CredentialVersion: 1, CredentialCiphertext: encrypted}}}
	s := &Server{store: st, cipher: cipher, openaiBalance: billingConfigProbe{}}
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"admin_key":"sk-admin-illustrative","organization_id":"org-example","grant_amount_usd":50,"grant_start_at":1800000000,"grant_expires_at":1800600000}`))
	r.SetPathValue("id", "example")
	w := httptest.NewRecorder()
	s.configureOpenAIBalance(w, r)
	if w.Code != 200 || !st.saved || strings.Contains(w.Body.String(), "sk-admin-illustrative") {
		t.Fatal("cost credential rejected or exposed")
	}
	plain, _ := cipher.Decrypt(st.account.CredentialCiphertext)
	var stored openaicompat.Credentials
	json.Unmarshal(plain, &stored)
	if stored.APIKey != "illustrative-model-key" || stored.OpenAIBilling == nil || stored.OpenAIBilling.AdminKey != "sk-admin-illustrative" || stored.OpenAIBilling.SessionKey != "" {
		t.Fatal("incorrect Costs credential persistence")
	}
}

func TestBillingRouteRequiresAdmin(t *testing.T) {
	mux := http.NewServeMux()
	s := &Server{}
	s.Register(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/api/v1/provider-accounts/example/openai-balance", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatal("billing auth endpoint exposed")
	}
}

func TestGrantVerificationKeepsCostsCredentialsAndSnapshot(t *testing.T) {
	cipher, _ := credential.New(make([]byte, 32))
	current := openaicompat.Credentials{
		BaseURL: "https://api.openai.com/v1", APIKey: "example-model-key",
		OpenAIBilling: &openaibilling.Credentials{
			OrganizationID: "org-example", AdminKey: "sk-admin-example",
			GrantAmount: 50, GrantStartAt: 1800000000, GrantExpiresAt: 1800600000,
		},
	}
	raw, _ := json.Marshal(current)
	encrypted, _ := cipher.Encrypt(raw)
	quota := json.RawMessage(`{"plan_type":"openai_api_credits","balance":{"estimated":true,"remaining_usd":37.5}}`)
	st := &quotaConfigStore{controlStore: controlStore{account: domain.ProviderAccount{
		ID: "example", Provider: domain.ProviderOpenAICompatible, CredentialVersion: 1,
		CredentialCiphertext: encrypted, QuotaSnapshot: quota,
	}}}
	s := &Server{store: st, cipher: cipher, openaiBalance: billingConfigProbe{}}
	for _, tc := range []struct {
		org  string
		code int
	}{{"org-other", http.StatusBadRequest}, {"org-example", http.StatusOK}} {
		r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"verify_grants":true,"session_key":"example-browser-session","organization_id":"`+tc.org+`"}`))
		r.SetPathValue("id", "example")
		w := httptest.NewRecorder()
		s.configureOpenAIBalance(w, r)
		if w.Code != tc.code || st.saved != (tc.code == http.StatusOK) || strings.Contains(w.Body.String(), "example-browser-session") {
			t.Fatalf("unexpected authorization response: %d %s", w.Code, w.Body.String())
		}
	}
	plain, _ := cipher.Decrypt(st.account.CredentialCiphertext)
	var stored openaicompat.Credentials
	if err := json.Unmarshal(plain, &stored); err != nil {
		t.Fatal(err)
	}
	expected := *current.OpenAIBilling
	expected.GrantSessionKey = "example-browser-session"
	if stored.OpenAIBilling == nil || *stored.OpenAIBilling != expected || stored.APIKey != current.APIKey || string(st.account.QuotaSnapshot) != string(quota) {
		t.Fatal("verification changed Costs credentials, inference, or the saved estimate")
	}
}
