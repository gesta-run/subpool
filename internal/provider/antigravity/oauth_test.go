package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOAuthPKCEStateAndSingleUse(t *testing.T) {
	var challenge string
	tokens := 0
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokens++
			_ = r.ParseForm()
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge || r.Form.Get("code") != "example-code" || r.Form.Get("redirect_uri") != DefaultRedirectURI {
				t.Error("PKCE or code mismatch")
			}
			if r.Form.Get("client_id") != "example-client" || r.Form.Get("client_secret") != "example-secret" {
				t.Error("token exchange did not use the configured OAuth client")
			}
			_, _ = io.WriteString(w, `{"access_token":"example-access","refresh_token":"example-refresh","expires_in":3600}`)
		case "/userinfo":
			_, _ = io.WriteString(w, `{"sub":"example-subject","email":"user@example.test"}`)
		case "/v1internal:loadCodeAssist":
			_, _ = io.WriteString(w, `{"cloudaicompanionProject":"example-project"}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	oauth := NewOAuth(client)
	login, err := oauth.Start()
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(login.URL)
	params := parsed.Query()
	challenge = params.Get("code_challenge")
	if params.Get("state") != login.LoginID || params.Get("code_challenge_method") != "S256" || params.Get("access_type") != "offline" || params.Get("prompt") != "consent" {
		t.Fatal("missing OAuth protection")
	}
	callback := DefaultRedirectURI + "?code=example-code&state=" + login.LoginID
	if _, err = oauth.Complete(context.Background(), login.LoginID, DefaultRedirectURI+"?code=example-code&state=wrong"); err == nil {
		t.Fatal("mismatched state accepted")
	}
	if _, err = oauth.Complete(context.Background(), login.LoginID, "https://example.test/callback?code=example-code&state="+login.LoginID); err == nil {
		t.Fatal("wrong redirect accepted")
	}
	credentials, err := oauth.Complete(context.Background(), login.LoginID, callback)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Subject != "example-subject" || credentials.ProjectID != "example-project" {
		t.Fatal("identity/project discovery failed")
	}
	if _, err = oauth.Complete(context.Background(), login.LoginID, callback); err == nil || tokens != 1 {
		t.Fatal("OAuth session replayed")
	}
}

func TestOAuthUsesConfiguredClientAndFixedCallback(t *testing.T) {
	t.Setenv("SUBPOOL_ANTIGRAVITY_REDIRECT_URI", "https://example.test/ignored")
	oauth := NewOAuth(New(Config{ClientID: " example-client ", ClientSecret: " example-secret "}))
	first, err := oauth.Start()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(first.URL)
	if err != nil {
		t.Fatal(err)
	}
	params := parsed.Query()
	if parsed.Scheme != "https" || parsed.Host != "accounts.google.com" || params.Get("client_id") != "example-client" || params.Get("redirect_uri") != DefaultRedirectURI {
		t.Fatal("authorization did not use the configured client and fixed callback")
	}
	if params.Get("client_secret") != "" || strings.Contains(first.URL, "example-secret") {
		t.Fatal("authorization URL exposed the client secret")
	}
	if params.Get("state") != first.LoginID || params.Get("code_challenge_method") != "S256" || params.Get("code_challenge") == "" {
		t.Fatal("authorization omitted state or PKCE")
	}
	if time.Until(first.ExpiresAt) < 9*time.Minute || time.Until(first.ExpiresAt) > 10*time.Minute {
		t.Fatal("unexpected authorization lifetime")
	}
	second, err := oauth.Start()
	if err != nil {
		t.Fatal(err)
	}
	secondURL, _ := url.Parse(second.URL)
	if second.LoginID == first.LoginID || secondURL.Query().Get("code_challenge") == params.Get("code_challenge") {
		t.Fatal("authorization reused state or PKCE verifier")
	}
}

func TestOAuthExpiryAndPendingBound(t *testing.T) {
	oauth := NewOAuth(New(Config{ClientID: "example-client", ClientSecret: "example-secret"}))
	oauth.pending["expired"] = pendingLogin{verifier: "example-verifier", expires: time.Now().Add(-time.Minute)}
	if _, err := oauth.Complete(context.Background(), "expired", DefaultRedirectURI+"?code=example-code&state=expired"); err == nil {
		t.Fatal("expired OAuth accepted")
	}
	for range 128 {
		if _, err := oauth.Start(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := oauth.Start(); err == nil {
		t.Fatal("pending authorization bound not enforced")
	}
}

func TestOAuthRequiresConfiguredClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("unconfigured OAuth reached the upstream")
	}))
	defer server.Close()
	for _, config := range []Config{
		{}, {ClientID: "example-client"}, {ClientSecret: "example-secret"},
		{ClientID: " ", ClientSecret: "example-secret"}, {ClientID: "example-client", ClientSecret: " "},
	} {
		config.TokenURL, config.HTTPClient = server.URL, server.Client()
		client := New(config)
		oauth := NewOAuth(client)
		if _, err := oauth.Start(); err != errOAuthNotConfigured || len(oauth.pending) != 0 {
			t.Fatal("unconfigured OAuth created an authorization session")
		}
		if _, err := client.Refresh(context.Background(), Credentials{RefreshToken: "example-refresh"}); err != errOAuthNotConfigured {
			t.Fatal("unconfigured refresh did not return a configuration error")
		}
	}
}

func TestRefreshRequiresAccountToken(t *testing.T) {
	client := testClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("missing refresh token reached the upstream")
	})
	if _, err := client.Refresh(context.Background(), Credentials{}); err == nil || !strings.Contains(err.Error(), "authorize again") {
		t.Fatal("missing refresh token did not request reauthorization")
	}
}
