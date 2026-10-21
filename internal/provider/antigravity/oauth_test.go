package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
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

func TestOAuthConfigurationAndExpiry(t *testing.T) {
	if _, err := NewOAuth(New(Config{})).Start(); err == nil {
		t.Fatal("unconfigured OAuth accepted")
	}
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
