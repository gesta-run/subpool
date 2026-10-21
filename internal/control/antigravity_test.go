package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
)

type googleOAuthStub struct{ calls int }

func (*googleOAuthStub) Start() (antigravity.Authorization, error) {
	return antigravity.Authorization{LoginID: "example-login", URL: "https://accounts.google.com/example", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (s *googleOAuthStub) Complete(context.Context, string, string) (antigravity.Credentials, error) {
	s.calls++
	return antigravity.Credentials{AccessToken: "example-access", RefreshToken: "example-refresh", Subject: "example-subject", ProjectID: "example-project", Email: "user@example.test"}, nil
}

func TestAntigravityAuthorizationRequiresAdminAndEncryptsCredentials(t *testing.T) {
	server, st, cipher := newControlServer(t)
	server.antigravityOAuth = &googleOAuthStub{}
	mux := http.NewServeMux()
	server.Register(mux)
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest("POST", "/api/v1/provider-accounts/antigravity/oauth", nil))
	if unauthorized.Code != 401 {
		t.Fatal("OAuth was exposed without admin authorization")
	}
	cookie := loginCookie(t, mux)
	start := httptest.NewRequest("POST", "/api/v1/provider-accounts/antigravity/oauth", nil)
	start.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, start)
	if recorder.Code != 201 || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("start failed: %d %s", recorder.Code, recorder.Body.String())
	}
	request := httptest.NewRequest("POST", "/api/v1/provider-accounts/antigravity/oauth/complete", strings.NewReader(`{"login_id":"example-login","callback_url":"http://localhost:8085/callback?code=example-code","display_name":"Example Google account"}`))
	request.AddCookie(cookie)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != 201 || st.account.Provider != domain.ProviderAntigravity || st.account.CredentialType != domain.CredentialSubscription {
		t.Fatalf("account not saved: %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(string(st.account.CredentialCiphertext), "example-access") || strings.Contains(recorder.Body.String(), "example-refresh") {
		t.Fatal("credential was exposed")
	}
	plain, err := cipher.Decrypt(st.account.CredentialCiphertext)
	var credentials antigravity.Credentials
	if err != nil || json.Unmarshal(plain, &credentials) != nil || credentials.ProjectID != "example-project" {
		t.Fatal("credential could not be restored")
	}
}
