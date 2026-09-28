package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientRefreshesRejectedTokenAndCachesReplacement(t *testing.T) {
	var exchanges atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			sequence := exchanges.Add(1)
			_, _ = fmt.Fprintf(w, `{"token":"copilot-%d","expires_at":%d,"refresh_in":3600,"endpoints":{"api":%q}}`, sequence, time.Now().Add(time.Hour).Unix(), "http://"+r.Host)
		case "/chat/completions":
			requests.Add(1)
			if r.Header.Get("Authorization") == "Bearer copilot-1" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("copilot-integration-id") != "vscode-chat" || r.Header.Get("X-Initiator") != "user" {
				t.Errorf("missing Copilot headers: %#v", r.Header)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"model":"gpt-test"}` {
				t.Errorf("body = %s", body)
			}
			_, _ = io.WriteString(w, `{"id":"chat-1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(ClientConfig{APIBase: server.URL, TokenExchangeURL: server.URL + "/token", HTTPClient: server.Client()})
	credentials := Credentials{GitHubToken: "github-token", Login: "octocat", UserID: 1}
	for range 2 {
		response, err := client.ChatCompletions(context.Background(), []byte(`{"model":"gpt-test"}`), nil, credentials)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
	}
	if exchanges.Load() != 2 || requests.Load() != 3 {
		t.Fatalf("exchanges/requests = %d/%d, want 2/3", exchanges.Load(), requests.Load())
	}
}

func TestClientDoesNotRefreshForbiddenResponse(t *testing.T) {
	var exchanges atomic.Int32
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			exchanges.Add(1)
			_, _ = fmt.Fprintf(w, `{"token":"copilot-token","expires_at":%d,"refresh_in":3600,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), "http://"+r.Host)
		case "/chat/completions":
			requests.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"message":"model is not available"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(ClientConfig{APIBase: server.URL, TokenExchangeURL: server.URL + "/token", HTTPClient: server.Client()})
	response, err := client.ChatCompletions(context.Background(), []byte(`{"model":"restricted-model"}`), nil, Credentials{GitHubToken: "github-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden || exchanges.Load() != 1 || requests.Load() != 1 {
		t.Fatalf("status/exchanges/requests = %d/%d/%d", response.StatusCode, exchanges.Load(), requests.Load())
	}
}

func TestClientDoesNotForwardCodexIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = fmt.Fprintf(w, `{"token":"copilot-token","expires_at":%d,"refresh_in":3600,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), "http://"+r.Host)
		case "/chat/completions":
			for _, name := range []string{
				"X-Codex-Installation-Id", "X-Codex-Turn-Metadata", "X-Codex-Window-Id",
				"X-Client-Request-Id", "Session-Id", "Thread-Id", "Chatgpt-Account-Id", "Originator",
			} {
				if value := r.Header.Get(name); value != "" {
					t.Errorf("%s leaked to Copilot: %q", name, value)
				}
			}
			if userAgent := r.Header.Get("User-Agent"); userAgent != "GitHubCopilotChat/0.26.7" {
				t.Errorf("User-Agent = %q", userAgent)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode Copilot request body: %v", err)
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			for _, field := range []string{"client_metadata", "x-codex-installation-id", "x-codex-turn-metadata"} {
				if _, exists := payload[field]; exists {
					t.Errorf("body field %q leaked to Copilot", field)
				}
			}
			if payload["model"] != "gpt-test" || payload["stream"] != true {
				t.Errorf("payload = %#v", payload)
			}
			_, _ = io.WriteString(w, `{"id":"chat-1"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(ClientConfig{APIBase: server.URL, TokenExchangeURL: server.URL + "/token", HTTPClient: server.Client()})
	body := []byte(`{"model":"gpt-test","stream":true,"client_metadata":{"session_id":"session-1","x-codex-installation-id":"installation-1"},"x-codex-installation-id":"installation-1","x-codex-turn-metadata":"turn-1"}`)
	headers := http.Header{
		"Accept":                  {"text/event-stream"},
		"User-Agent":              {"codex-tui/0.146.0"},
		"Originator":              {"codex_cli_rs"},
		"Chatgpt-Account-Id":      {"chatgpt-account"},
		"X-Codex-Installation-Id": {"installation-1"},
		"X-Codex-Turn-Metadata":   {"turn-1"},
		"X-Codex-Window-Id":       {"window-1"},
		"X-Client-Request-Id":     {"request-1"},
		"Session-Id":              {"session-1"},
		"Thread-Id":               {"thread-1"},
	}
	response, err := client.ChatCompletions(context.Background(), body, headers, Credentials{GitHubToken: "github-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestDecodeModels(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"data":[{"id":"gpt-test","name":"GPT Test","display_name":"GPT Test"}]}`)),
	}
	models, err := DecodeModels(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gpt-test" {
		t.Fatalf("models = %#v", models)
	}
}

func TestClientReadsAICreditsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = fmt.Fprintf(w, `{"token":"copilot-token","expires_at":%d}`, time.Now().Add(time.Hour).Unix())
			return
		}
		if r.URL.Path != "/copilot_internal/user" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "token github-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{
			"copilot_plan":"pro_plus",
			"quota_reset_date":"2026-10-01",
			"quota_snapshots":{"premium_interactions":{
				"entitlement":1500,"credits_used":375,"remaining":1125,"percent_remaining":75,
				"overage_permitted":false,"overage_count":0
			}}
		}`)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{TokenExchangeURL: server.URL + "/token", EntitlementsURL: server.URL + "/copilot_internal/user", HTTPClient: server.Client()})
	snapshot, err := client.Credits(context.Background(), Credentials{GitHubToken: "github-token"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlanType != "pro_plus" || snapshot.Credits == nil || snapshot.UsageAllowed == nil || !*snapshot.UsageAllowed {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	credits := snapshot.Credits
	if credits.Used != 375 || credits.Entitlement != 1500 || credits.Remaining != 1125 || credits.RemainingPercent != 75 || credits.ResetAt != 1790812800 {
		t.Fatalf("credits = %#v", credits)
	}
}

func TestClientMarksExhaustedAICreditsUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_, _ = fmt.Fprintf(w, `{"token":"copilot-token","expires_at":%d}`, time.Now().Add(time.Hour).Unix())
			return
		}
		_, _ = io.WriteString(w, `{"quota_snapshots":{"premium_interactions":{"entitlement":1500,"percent_remaining":0,"overage_permitted":false}}}`)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{TokenExchangeURL: server.URL + "/token", EntitlementsURL: server.URL, HTTPClient: server.Client()})
	snapshot, err := client.Credits(context.Background(), Credentials{GitHubToken: "github-token"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Credits == nil || snapshot.UsageAllowed == nil || *snapshot.UsageAllowed || snapshot.Credits.Used != 1500 || snapshot.Credits.Remaining != 0 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestExchangeTokenDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"token":"sensitive-value"}`)
	}))
	defer server.Close()
	_, err := exchangeToken(context.Background(), server.Client(), server.URL, "github-token", time.Now())
	if err == nil || strings.Contains(err.Error(), "sensitive-value") {
		t.Fatalf("error = %v", err)
	}
}
