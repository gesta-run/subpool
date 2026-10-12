package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

func TestCompatibleFallbackOnlyForMissingEndpoints(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		fallback bool
	}{
		{"missing route", 404, `{"error":{"message":"endpoint not found"}}`, true},
		{"method unavailable", 405, `{"error":{"message":"method not allowed"}}`, true},
		{"not implemented", 501, `{"error":{"message":"not implemented"}}`, true},
		{"missing model", 404, `{"error":{"code":"model_not_found","message":"unknown model"}}`, false},
		{"invalid request", 400, `{"error":{"message":"invalid parameters"}}`, false},
		{"authentication", 401, `{"error":{"message":"unauthorized"}}`, false},
		{"forbidden", 403, `{"error":{"message":"forbidden"}}`, false},
		{"rate limit", 429, `{"error":{"message":"rate limit"}}`, false},
		{"server error", 500, `{"error":{"message":"upstream unavailable"}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/responses" {
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, test.body)
					return
				}
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				_, _ = io.WriteString(w, `{"id":"chatcmpl-illustrative","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
			}))
			defer upstream.Close()
			provider := compatibleAdapter{client: openaicompat.NewClient(upstream.Client())}
			response, err := provider.RoundTrip(context.Background(), Request{Kind: "responses", Body: []byte(`{"model":"illustrative-model","input":"hello"}`)}, nil,
				Credentials{Compatible: openaicompat.Credentials{BaseURL: upstream.URL + "/v1", APIKey: "illustrative-key"}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			wantCalls, wantStatus := 1, test.status
			if test.fallback {
				wantCalls, wantStatus = 2, http.StatusOK
			}
			if calls != wantCalls || response.StatusCode != wantStatus {
				t.Fatalf("calls=%d status=%d", calls, response.StatusCode)
			}
			if !test.fallback && string(body) != test.body {
				t.Fatalf("original error body changed: %s", body)
			}
		})
	}
}

func TestCompatibleFallbackRejectsLossyRequests(t *testing.T) {
	for _, body := range []string{
		`{"model":"illustrative-model","input":"hello","previous_response_id":"resp_illustrative"}`,
		`{"model":"illustrative-model","input":"hello","context_management":[{"type":"compaction","compact_threshold":1000}]}`,
		`{"model":"illustrative-model","input":[{"type":"function_call_output","call_id":"call_illustrative","output":[{"type":"input_image","image_url":"https://example.invalid/fixture.png"}]}]}`,
	} {
		calls := 0
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/v1/responses" {
				t.Errorf("lossy fallback reached %s", r.URL.Path)
			}
			http.NotFound(w, r)
		}))
		provider := compatibleAdapter{client: openaicompat.NewClient(upstream.Client())}
		_, err := provider.RoundTrip(context.Background(), Request{Kind: "responses", Body: []byte(body)}, nil,
			Credentials{Compatible: openaicompat.Credentials{BaseURL: upstream.URL + "/v1", APIKey: "illustrative-key"}}, Options{})
		upstream.Close()
		var unsupported *RequestError
		if !errors.As(err, &unsupported) || calls != 1 {
			t.Fatalf("lossy request calls=%d error=%v", calls, err)
		}
	}
}

func TestEndpointDetectionPreservesLongModelError(t *testing.T) {
	body := `{"error":{"code":"model_not_found","message":"` + strings.Repeat("illustrative", 10000) + `"}}`
	response := &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(body))}
	defer response.Body.Close()
	if endpointUnavailable(response) {
		t.Fatal("a model error was treated as an endpoint capability failure")
	}
	got, err := io.ReadAll(response.Body)
	if err != nil || string(got) != body {
		t.Fatalf("error body was not preserved: length=%d error=%v", len(got), err)
	}
}

func TestCompatibleChatFallbackPreservesSamplingOptions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request["temperature"] != 0.25 || request["top_p"] != 0.9 {
			t.Errorf("sampling options lost: %v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"resp_illustrative","status":"completed","output":[]}}`+"\n\n")
	}))
	defer upstream.Close()
	provider := compatibleAdapter{client: openaicompat.NewClient(upstream.Client())}
	response, err := provider.RoundTrip(context.Background(), Request{Kind: "chat_completions", Body: []byte(`{"model":"illustrative-model","temperature":0.25,"top_p":0.9,"messages":[{"role":"user","content":"hello"}]}`)}, nil,
		Credentials{Compatible: openaicompat.Credentials{BaseURL: upstream.URL + "/v1", APIKey: "illustrative-key"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.format != "responses" {
		t.Fatalf("status=%d format=%s", response.StatusCode, response.format)
	}
}

func TestCompatibleChatFallbackRejectsLossyControls(t *testing.T) {
	for _, control := range []string{`"n":2`, `"stop":["ILLUSTRATIVE_END"]`, `"logit_bias":{"123":-100}`} {
		t.Run(control, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("lossy fallback reached %s", r.URL.Path)
				}
				http.NotFound(w, r)
			}))
			defer upstream.Close()
			provider := compatibleAdapter{client: openaicompat.NewClient(upstream.Client())}
			body := []byte(`{"model":"illustrative-model","messages":[{"role":"user","content":"hello"}],` + control + `}`)
			_, err := provider.RoundTrip(context.Background(), Request{Kind: "chat_completions", Body: body}, nil,
				Credentials{Compatible: openaicompat.Credentials{BaseURL: upstream.URL + "/v1", APIKey: "illustrative-key"}}, Options{})
			var unsupported *RequestError
			if !errors.As(err, &unsupported) || unsupported.Code != "unsupported_parameter" || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestCompatibleNativeChatPreservesControls(t *testing.T) {
	body := []byte(`{"model":"illustrative-model","messages":[{"role":"user","content":"hello"}],"n":2,"stop":["ILLUSTRATIVE_END"],"logit_bias":{"123":-100}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		got, _ := io.ReadAll(r.Body)
		if string(got) != string(body) {
			t.Errorf("native Chat request changed: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-illustrative","choices":[]}`)
	}))
	defer upstream.Close()
	provider := compatibleAdapter{client: openaicompat.NewClient(upstream.Client())}
	response, err := provider.RoundTrip(context.Background(), Request{Kind: "chat_completions", Body: body}, nil,
		Credentials{Compatible: openaicompat.Credentials{BaseURL: upstream.URL + "/v1", APIKey: "illustrative-key"}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("native Chat controls rejected: status=%d", response.StatusCode)
	}
}
