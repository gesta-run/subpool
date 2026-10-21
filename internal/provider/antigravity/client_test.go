package antigravity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Config{ClientID: "example-client", ClientSecret: "example-secret", BaseURL: server.URL,
		AuthorizeURL: server.URL + "/authorize", TokenURL: server.URL + "/token", UserInfoURL: server.URL + "/userinfo", HTTPClient: server.Client()})
}

func TestGenerateWrapsInternalProtocol(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("unexpected endpoint: %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer example-access" || !strings.HasPrefix(r.Header.Get("User-Agent"), "antigravity/hub/") {
			t.Error("missing upstream identity headers")
		}
		if r.Header.Get("X-Client-Secret") != "" {
			t.Error("client headers leaked")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["project"] != "example-project" || body["model"] != "gemini-3-flash" || body["requestType"] != "agent" || body["request"] == nil {
			t.Errorf("invalid envelope: %#v", body)
		}
		_, _ = io.WriteString(w, "data: {}\n\n")
	})
	response, err := client.Generate(context.Background(), "gemini-3-flash", json.RawMessage(`{"contents":[]}`), Credentials{AccessToken: "example-access", ProjectID: "example-project"})
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
}

func TestRefreshPreservesIdentityAndRefreshToken(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "example-refresh" || r.Form.Get("client_secret") != "example-secret" {
			t.Error("invalid refresh form")
		}
		_, _ = io.WriteString(w, `{"access_token":"example-new-access","expires_in":3600}`)
	})
	current := Credentials{AccessToken: "old", RefreshToken: "example-refresh", Subject: "example-subject", ProjectID: "example-project", Email: "user@example.test"}
	result, err := client.Refresh(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken != "example-new-access" || result.RefreshToken != current.RefreshToken || result.Subject != current.Subject || result.ProjectID != current.ProjectID || result.Email != current.Email || time.Until(result.ExpiresAt) < 59*time.Minute {
		t.Errorf("invalid refresh result: identity preserved=%v", result.Subject == current.Subject)
	}
}

func TestOAuthErrorsDoNotExposeUpstreamBody(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"example-private-token"}`)
	})
	_, err := client.Refresh(context.Background(), Credentials{RefreshToken: "example-refresh"})
	if err == nil || !IsDefinitiveAuthError(err) || strings.Contains(err.Error(), "example-private-token") {
		t.Fatalf("unsafe or misclassified error: %v", err)
	}
	if IsDefinitiveAuthError(&HTTPError{StatusCode: 403}) || IsDefinitiveAuthError(&HTTPError{StatusCode: 429}) {
		t.Fatal("eligibility and rate limits are not revoked credentials")
	}
}

func TestCredentialsNeverFollowRedirects(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	})
	_, err := client.Models(context.Background(), Credentials{AccessToken: "example-access", ProjectID: "example-project"})
	if err == nil || called {
		t.Fatalf("credentials followed redirect: called=%v err=%v", called, err)
	}
}

func TestProjectOnboardingAndQuota(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = io.WriteString(w, `{"allowedTiers":[{"id":"example-free","isDefault":true}]}`)
		case "/v1internal:onboardUser":
			var input map[string]any
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["tierId"] != "example-free" {
				t.Error("wrong tier")
			}
			_, _ = io.WriteString(w, `{"done":true,"response":{"cloudaicompanionProject":{"id":"example-project"}}}`)
		case "/v1internal:fetchAvailableModels":
			_, _ = io.WriteString(w, `{"models":{"gemini-3-flash":{"displayName":"Example Flash","quotaInfo":{"remainingFraction":0.75,"resetTime":"2030-01-01T00:00:00Z"}}}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	project, err := client.project(context.Background(), "example-access")
	if err != nil || project != "example-project" {
		t.Fatalf("project=%s err=%v", project, err)
	}
	snapshot, err := client.Models(context.Background(), Credentials{AccessToken: "example-access", ProjectID: project})
	if err != nil || snapshot.Models["gemini-3-flash"].Quota.RemainingFraction != 0.75 {
		t.Fatalf("invalid quota: %v", err)
	}
}
