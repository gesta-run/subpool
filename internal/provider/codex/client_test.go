package codex

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		for name, want := range map[string]string{"Authorization": "Bearer access", "Chatgpt-Account-Id": "acct", "Originator": "codex_cli_rs", "Accept": "text/event-stream", "Session-Id": "session", "X-Codex-Installation-Id": "installation"} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"gpt-test"}` {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewClient(server.URL, http.DefaultClient)
	headers := http.Header{"Session-Id": []string{"session"}, "X-Codex-Installation-Id": []string{"installation"}}
	resp, err := client.Responses(context.Background(), []byte(`{"model":"gpt-test"}`), headers, Credentials{AccessToken: "access", AccountID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestSetRoutingHint(t *testing.T) {
	headers := http.Header{"x-codex-routing-hint": {"spoofed"}}
	SetRoutingHint(headers, "gpt-5.6-sol", true)
	if got := headers.Get(RoutingHintHeader); got != "model=gpt-5.6-sol;tier=priority" {
		t.Fatalf("routing hint = %q", got)
	}
	SetRoutingHint(headers, "gpt-5.6-sol", false)
	if got := headers.Get(RoutingHintHeader); got != "model=gpt-5.6-sol" {
		t.Fatalf("standard routing hint = %q", got)
	}
	SetRoutingHint(headers, "invalid;model", false)
	if got := headers.Get(RoutingHintHeader); got != "" {
		t.Fatalf("invalid model routing hint = %q", got)
	}
}

func TestClientUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usage" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access" || r.Header.Get("Chatgpt-Account-Id") != "acct" {
			t.Errorf("headers = %#v", r.Header)
		}
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":25,"limit_window_seconds":18000,"reset_at":1800000000},"secondary_window":{"used_percent":40,"limit_window_seconds":604800,"reset_at":1800500000}}}`))
	}))
	defer server.Close()

	snapshot, err := NewClient(server.URL, http.DefaultClient).Usage(context.Background(), Credentials{AccessToken: "access", AccountID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PlanType != "plus" || snapshot.FiveHour == nil || snapshot.FiveHour.RemainingPercent != 75 || snapshot.Weekly == nil || snapshot.Weekly.RemainingPercent != 60 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestClientUsageDerivesChatGPTEndpoint(t *testing.T) {
	client := NewClient("https://chatgpt.com/backend-api/codex", http.DefaultClient)
	if client.usageURL != "https://chatgpt.com/backend-api/wham/usage" {
		t.Fatalf("usage URL = %s", client.usageURL)
	}
}

func TestClientUsageReturnsStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	_, err := NewClient(server.URL, http.DefaultClient).Usage(context.Background(), Credentials{AccessToken: "access"})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want HTTP status 429", err)
	}
}
