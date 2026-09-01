package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestOAuthStartAndExchange(t *testing.T) {
	var tokenForm url.Values
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		tokenForm = r.Form
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "id_token": testIDToken(t, "acct-1", "employee@example.com"), "expires_in": 3600})
	}))
	defer tokenServer.Close()
	oauth := NewOAuth(OAuthConfig{ClientID: "client", AuthURL: "https://auth.example/authorize", TokenURL: tokenServer.URL, RedirectURL: "https://subpool.example/callback"})
	authURL, err := oauth.Start("Primary", 3)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(authURL)
	query := parsed.Query()
	for key, want := range map[string]string{"client_id": "client", "response_type": "code", "redirect_uri": "https://subpool.example/callback", "scope": oauthScope, "code_challenge_method": "S256", "id_token_add_organizations": "true", "codex_cli_simplified_flow": "true"} {
		if got := query.Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Get("code_challenge") == "" || query.Get("state") == "" {
		t.Fatal("PKCE or state is missing")
	}
	credentials, display, maxKeys, err := oauth.Exchange(context.Background(), query.Get("state"), "authorization-code")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessToken != "access" || credentials.RefreshToken != "refresh" || credentials.AccountID != "acct-1" || credentials.Email != "employee@example.com" {
		t.Fatalf("unexpected credentials: %#v", credentials)
	}
	if display != "Primary" || maxKeys != 3 {
		t.Fatalf("metadata = %q, %d", display, maxKeys)
	}
	for key, want := range map[string]string{"grant_type": "authorization_code", "client_id": "client", "code": "authorization-code", "redirect_uri": "https://subpool.example/callback"} {
		if got := tokenForm.Get(key); got != want {
			t.Fatalf("token %s = %q", key, got)
		}
	}
	if tokenForm.Get("code_verifier") == "" {
		t.Fatal("code_verifier is missing")
	}
}

func TestOAuthRefreshPreservesRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("scope") != "" {
			t.Error("refresh request must not send scope")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "expires_in": 60})
	}))
	defer server.Close()
	oauth := NewOAuth(OAuthConfig{ClientID: "client", TokenURL: server.URL})
	credentials, err := oauth.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.RefreshToken != "old-refresh" {
		t.Fatalf("refresh token = %q", credentials.RefreshToken)
	}
}

func TestOAuthRejectsInvalidCapacity(t *testing.T) {
	oauth := NewOAuth(OAuthConfig{})
	for _, value := range []int{-1, 101} {
		if _, err := oauth.Start("", value); err == nil {
			t.Fatalf("capacity %d accepted", value)
		}
	}
}

func TestRefreshErrorClassification(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body       string
		definitive bool
	}{
		{name: "invalid grant", status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`, definitive: true},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"code":"expired_token"}}`, definitive: true},
		{name: "server error", status: http.StatusBadGateway, body: `{"error":"temporarily_unavailable"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			oauth := NewOAuth(OAuthConfig{ClientID: "client", TokenURL: server.URL})
			_, err := oauth.Refresh(context.Background(), "refresh")
			if err == nil || IsDefinitiveAuthError(err) != test.definitive {
				t.Fatalf("error=%v definitive=%v", err, IsDefinitiveAuthError(err))
			}
		})
	}
}

func testIDToken(t *testing.T, accountID, email string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID}})
	return strings.Join([]string{base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)), base64.RawURLEncoding.EncodeToString(payload), "signature"}, ".")
}
