package copilot

import (
	"context"
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
