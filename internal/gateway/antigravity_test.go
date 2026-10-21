package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
)

type antigravityStub struct {
	calls     int
	body      []byte
	status    int
	stream    string
	errorBody string
}

func (p *antigravityStub) Generate(_ context.Context, _ string, body json.RawMessage, _ antigravity.Credentials) (*http.Response, error) {
	p.calls++
	p.body = append([]byte(nil), body...)
	if p.status != 0 {
		body := p.errorBody
		if body == "" {
			body = `{"error":{"details":[{"retryDelay":"30s"}]}}`
		}
		return &http.Response{StatusCode: p.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	stream := p.stream
	if stream == "" {
		stream = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":7,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":2}}}\n\n"
	}
	return sseResponse(200, stream), nil
}

func TestAntigravityModelCapacityIsNotAnAccountFailure(t *testing.T) {
	server, st, provider, key := antigravityTestServer(t)
	provider.status = http.StatusServiceUnavailable
	provider.errorBody = `{"error":{"details":[{"reason":"MODEL_CAPACITY_EXHAUSTED"},{"retryDelay":"2s"}]}}`
	_ = serveGateway(t, server, key, "/v1/responses", `{"model":"gemini-3-flash","input":"Hello"}`)
	if len(st.status) != 0 {
		t.Fatalf("model capacity changed account status: %v", st.status)
	}
	provider.status = 0
	recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"gemini-3-pro","input":"Hello"}`)
	if recorder.Code != 200 {
		t.Fatalf("other models were affected: %d", recorder.Code)
	}
}

func antigravityTestServer(t *testing.T) (*Server, *fakeStore, *antigravityStub, string) {
	t.Helper()
	server, st, _, key := newTestServer(t)
	raw, _ := json.Marshal(antigravity.Credentials{AccessToken: "example-access", RefreshToken: "example-refresh", ProjectID: "example-project", Subject: "example-subject", ExpiresAt: time.Now().Add(time.Hour)})
	encrypted, err := server.cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	account := domain.ProviderAccount{ID: "example-account", Provider: domain.ProviderAntigravity, CredentialType: domain.CredentialSubscription, Status: domain.AccountActive, CredentialVersion: 1, CredentialCiphertext: encrypted}
	st.route.Account, st.sessionAccount = account, account
	st.route.Pool.Provider = domain.ProviderAntigravity
	provider := &antigravityStub{}
	server.WithAntigravity(provider)
	return server, st, provider, key
}

func TestAntigravityWireMatrixMetersExactlyOnce(t *testing.T) {
	for _, kind := range []string{"responses", "chat/completions"} {
		for _, stream := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
				server, st, provider, key := antigravityTestServer(t)
				input := map[string]any{"model": "gemini-3-flash", "stream": stream}
				if kind == "responses" {
					input["input"] = "Hello"
				} else {
					input["messages"] = []any{map[string]string{"role": "user", "content": "Hello"}}
				}
				raw, _ := json.Marshal(input)
				recorder := serveGateway(t, server, key, "/v1/"+kind, string(raw))
				if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "Hello") || provider.calls != 1 || st.usageInput != 7 || st.usageOutput != 5 {
					t.Fatalf("status=%d calls=%d usage=%d/%d body=%s", recorder.Code, provider.calls, st.usageInput, st.usageOutput, recorder.Body.String())
				}
				if kind == "responses" && !st.sessionSaved {
					t.Fatal("response was not bound to the account")
				}
				if stream && kind == "responses" && strings.Count(recorder.Body.String(), `"type":"response.completed"`) != 1 {
					t.Fatal("terminal response count differs from one")
				}
			})
		}
	}
}

func TestAntigravityRateLimitDoesNotDisableOtherModels(t *testing.T) {
	server, st, provider, key := antigravityTestServer(t)
	provider.status = 429
	_ = serveGateway(t, server, key, "/v1/responses", `{"model":"gemini-3-flash","input":"Hello"}`)
	if len(st.status) != 0 {
		t.Fatalf("model 429 changed whole-account status: %v", st.status)
	}
	_ = serveGateway(t, server, key, "/v1/responses", `{"model":"gemini-3-flash","input":"Hello"}`)
	if provider.calls != 1 {
		t.Fatal("model cooldown was ignored")
	}
	provider.status = 0
	recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"gemini-3-pro","input":"Hello"}`)
	if recorder.Code != 200 || provider.calls != 2 {
		t.Fatalf("other model was blocked: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAntigravityUnsupportedRequestDoesNotReachUpstream(t *testing.T) {
	for _, input := range []string{
		`{"model":"gemini-3-flash","input":"Hello","tools":[{"type":"web_search"}]}`,
		`{"model":"gemini-3-flash","input":[{"type":"compaction_trigger"}]}`,
		`{"model":"gemini-3-flash","input":"Hello","context_management":[]}`,
	} {
		server, _, provider, key := antigravityTestServer(t)
		recorder := serveGateway(t, server, key, "/v1/responses", input)
		if recorder.Code != 400 || provider.calls != 0 {
			t.Fatalf("unsupported request forwarded: status=%d calls=%d %s", recorder.Code, provider.calls, recorder.Body.String())
		}
	}
}

func TestAntigravityRefreshBeforeInference(t *testing.T) {
	server, st, provider, key := antigravityTestServer(t)
	valid := st.route.Account
	var current antigravity.Credentials
	plain, _ := server.cipher.Decrypt(valid.CredentialCiphertext)
	_ = json.Unmarshal(plain, &current)
	current.ExpiresAt = time.Now().Add(-time.Minute)
	raw, _ := json.Marshal(current)
	st.route.Account.CredentialCiphertext, _ = server.cipher.Encrypt(raw)
	refresher := &fakeRefresher{account: valid}
	server.refresher = refresher
	recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"gemini-3-flash","input":"Hello"}`)
	if recorder.Code != 200 || refresher.calls != 1 || provider.calls != 1 {
		t.Fatalf("refresh/inference failed: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestAntigravityModelUnavailableTriesFallback(t *testing.T) {
	for _, failure := range []string{
		`{"error":{"code":404,"message":"Model not found: example-private-detail"}}`,
		`{"error":{"code":"unsupported_model","message":"example-private-detail"}}`,
		`{"error":{"details":[{"reason":"MODEL_NOT_FOUND"}],"message":"example-private-detail"}}`,
	} {
		server, st, provider, key := antigravityTestServer(t)
		st.route.Pool.Provider = domain.ProviderMixed
		st.reassigned = accountWithCredentials(t, "fallback-account", "example-token")
		server.codex.(*fakeProvider).responses = []*http.Response{sseResponse(http.StatusOK,
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"example-response\",\"status\":\"completed\",\"output\":[]}}\n\n")}
		provider.status, provider.errorBody = http.StatusNotFound, failure
		recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"example-model","input":"Hello"}`)
		if recorder.Code != http.StatusOK || len(st.reassignExcludes) != 1 {
			t.Fatalf("model unavailability did not fall back: %d %s", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), "example-private-detail") || len(st.status) != 0 {
			t.Fatal("model unavailability leaked details or changed account status")
		}
	}
}

func TestAntigravityOtherClientErrorsDoNotTriggerModelFallback(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusForbidden} {
		server, st, provider, key := antigravityTestServer(t)
		st.route.Pool.Provider = domain.ProviderMixed
		st.reassigned = accountWithCredentials(t, "fallback-account", "example-token")
		provider.status, provider.errorBody = status, `{"error":{"message":"example-private-validation-detail"}}`
		recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"example-model","input":"Hello"}`)
		if recorder.Code != status || len(st.reassignExcludes) != 0 || strings.Contains(recorder.Body.String(), "example-private-validation-detail") {
			t.Fatalf("client error misclassified or exposed: %d %s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestAntigravityInvalidClaudeThinkingDoesNotReachUpstream(t *testing.T) {
	server, _, provider, key := antigravityTestServer(t)
	recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"claude-sonnet-4-5","input":"Hello","reasoning":{"effort":"high"},"max_output_tokens":2048}`)
	if recorder.Code != http.StatusBadRequest || provider.calls != 0 || !strings.Contains(recorder.Body.String(), "output token limit greater than") {
		t.Fatalf("invalid budget reached upstream: %d calls=%d %s", recorder.Code, provider.calls, recorder.Body.String())
	}
}
