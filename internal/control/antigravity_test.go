package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestAntigravityAuthorizationUsesConfiguredClient(t *testing.T) {
	server, _, _ := newControlServer(t)
	server.antigravityOAuth = antigravity.NewOAuth(antigravity.New(antigravity.Config{ClientID: "example-client", ClientSecret: "example-secret"}))
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest("POST", "/api/v1/provider-accounts/antigravity/oauth", nil)
	request.AddCookie(loginCookie(t, mux))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("authorization did not start: status %d", recorder.Code)
	}
	var authorization antigravity.Authorization
	if err := json.Unmarshal(recorder.Body.Bytes(), &authorization); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(authorization.URL)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	if parsed.Host != "accounts.google.com" || params.Get("client_id") != "example-client" || params.Get("redirect_uri") != antigravity.DefaultRedirectURI || params.Get("state") != authorization.LoginID || params.Get("code_challenge_method") != "S256" {
		t.Fatal("authorization response omitted the configured client or OAuth protections")
	}
	var fields map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || params.Get("client_secret") != "" || strings.Contains(recorder.Body.String(), "client_secret") || strings.Contains(recorder.Body.String(), "example-secret") {
		t.Fatal("authorization returned unexpected credential fields")
	}
}

func TestAntigravityAuthorizationRequiresOAuthConfiguration(t *testing.T) {
	for _, config := range []antigravity.Config{{}, {ClientID: "example-client"}, {ClientSecret: "example-secret"}} {
		server, _, _ := newControlServer(t)
		server.antigravityOAuth = antigravity.NewOAuth(antigravity.New(config))
		mux := http.NewServeMux()
		server.Register(mux)
		request := httptest.NewRequest("POST", "/api/v1/provider-accounts/antigravity/oauth", nil)
		request.AddCookie(loginCookie(t, mux))
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "SUBPOOL_ANTIGRAVITY_CLIENT_ID") || !strings.Contains(recorder.Body.String(), "SUBPOOL_ANTIGRAVITY_CLIENT_SECRET") {
			t.Fatal("unconfigured authorization did not explain the required environment variables")
		}
		if strings.Contains(recorder.Body.String(), "example-client") || strings.Contains(recorder.Body.String(), "example-secret") {
			t.Fatal("configuration error exposed OAuth client values")
		}
	}
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
