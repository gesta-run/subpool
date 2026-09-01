package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

type fakeStore struct {
	store.Store
	route                   domain.KeyRoute
	sessionAccount          domain.ProviderAccount
	sessionErr              error
	reassigned              domain.ProviderAccount
	usageInput, usageOutput int64
	sessionSaved            bool
	savedAccount            string
	status                  []string
	usageFailures           int
	sessionFailures         int
	usageCalls              int
	sessionCalls            int
	usageCommitUncertain    bool
	usageEvents             map[string]struct{}
}

func (f *fakeStore) ResolveAPIKey(context.Context, []byte) (domain.KeyRoute, error) {
	return f.route, nil
}
func (f *fakeStore) ResolveSessionAccount(context.Context, string, []byte) (domain.ProviderAccount, error) {
	return f.sessionAccount, f.sessionErr
}
func (f *fakeStore) SaveSessionBinding(_ context.Context, _, _ string, _ []byte, account string, _ time.Time) error {
	f.sessionCalls++
	if f.sessionFailures > 0 {
		f.sessionFailures--
		return errors.New("temporary session failure")
	}
	f.sessionSaved = true
	f.savedAccount = account
	return nil
}
func (f *fakeStore) ReassignAPIKey(context.Context, string, string, string) (domain.ProviderAccount, error) {
	if f.reassigned.ID == "" {
		return domain.ProviderAccount{}, store.ErrCapacityExhausted
	}
	return f.reassigned, nil
}
func (f *fakeStore) MarkProviderSuccess(context.Context, string) error { return nil }
func (f *fakeStore) TouchAPIKey(context.Context, string) error         { return nil }
func (f *fakeStore) AddUsage(_ context.Context, _ string, eventHash []byte, _ time.Time, input, output int64) error {
	f.usageCalls++
	if f.usageEvents == nil {
		f.usageEvents = make(map[string]struct{})
	}
	eventKey := string(eventHash)
	if _, exists := f.usageEvents[eventKey]; exists {
		return nil
	}
	if f.usageFailures > 0 {
		f.usageFailures--
		return errors.New("temporary usage failure")
	}
	f.usageInput += input
	f.usageOutput += output
	f.usageEvents[eventKey] = struct{}{}
	if f.usageCommitUncertain {
		f.usageCommitUncertain = false
		return errors.New("commit result is uncertain")
	}
	return nil
}
func (f *fakeStore) UpdateProviderStatus(_ context.Context, _ string, status string, _ *time.Time) error {
	f.status = append(f.status, status)
	return nil
}

type fakeProvider struct {
	responses   []*http.Response
	credentials []codex.Credentials
	bodies      [][]byte
}

func (f *fakeProvider) Responses(_ context.Context, body []byte, _ http.Header, credentials codex.Credentials) (*http.Response, error) {
	f.credentials = append(f.credentials, credentials)
	f.bodies = append(f.bodies, append([]byte(nil), body...))
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

type fakeRefresher struct {
	account domain.ProviderAccount
	calls   int
	err     error
}

func (f *fakeRefresher) RefreshAccount(context.Context, string, int) (domain.ProviderAccount, error) {
	f.calls++
	return f.account, f.err
}

func TestResponsesStreamMetersAndBindsSession(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"usage\":{\"input_tokens\":12,\"input_tokens_details\":{\"cached_tokens\":5},\"output_tokens\":4}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","stream":true,"input":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "response.completed") {
		t.Fatalf("stream was not proxied: %s", recorder.Body.String())
	}
	if st.usageInput != 12 || st.usageOutput != 4 {
		t.Fatalf("usage = %d/%d", st.usageInput, st.usageOutput)
	}
	if !st.sessionSaved || st.savedAccount != "account-1" {
		t.Fatalf("session binding = %v, %q", st.sessionSaved, st.savedAccount)
	}
	var upstream map[string]any
	_ = json.Unmarshal(provider.bodies[0], &upstream)
	if upstream["stream"] != true {
		t.Fatal("upstream request did not force streaming")
	}
}

func TestResponsesJSONMetersTerminalResponse(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-json\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":3}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK || st.usageInput != 8 || st.usageOutput != 3 || len(st.usageEvents) != 1 {
		t.Fatalf("response=%d usage=%d/%d events=%d body=%s", recorder.Code, st.usageInput, st.usageOutput, len(st.usageEvents), recorder.Body.String())
	}
}

func TestContinuationUsesPersistedSessionAccount(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.sessionAccount = accountWithCredentials(t, "old-account", "old-token")
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"output\":[],\"usage\":{}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","previous_response_id":"resp-1","input":"continue"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if got := provider.credentials[0].AccessToken; got != "old-token" {
		t.Fatalf("used token %q", got)
	}
}

func TestUnknownContinuationIsRejected(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.sessionErr = store.ErrNotFound
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","previous_response_id":"unknown","input":"continue"}`)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "subpool_session_account_unavailable") {
		t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
	}
	if len(provider.credentials) != 0 {
		t.Fatal("provider was called")
	}
}

func TestRateLimitedAccountReassignsBeforeStreaming(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.reassigned = accountWithCredentials(t, "account-2", "token-2")
	provider.responses = []*http.Response{sseResponse(http.StatusTooManyRequests, "limited"), sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"output\":[],\"usage\":{}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(provider.credentials) != 2 || provider.credentials[1].AccessToken != "token-2" {
		t.Fatalf("credentials = %#v", provider.credentials)
	}
	if len(st.status) == 0 || st.status[0] != domain.AccountCoolingDown {
		t.Fatalf("statuses = %#v", st.status)
	}
}

func TestUnauthorizedRefreshesOnce(t *testing.T) {
	server, _, provider, plain := newTestServer(t)
	refresher := server.refresher.(*fakeRefresher)
	refresher.account = accountWithCredentials(t, "account-1", "new-token")
	provider.responses = []*http.Response{sseResponse(http.StatusUnauthorized, "unauthorized"), sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[],\"usage\":{}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if refresher.calls != 1 || provider.credentials[1].AccessToken != "new-token" {
		t.Fatalf("refresh calls=%d credentials=%#v", refresher.calls, provider.credentials)
	}
}

func TestReassignedUnauthorizedAccountRefreshesOnce(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.reassigned = accountWithCredentials(t, "account-2", "expired-token")
	refresher := server.refresher.(*fakeRefresher)
	refresher.account = accountWithCredentials(t, "account-2", "new-token")
	provider.responses = []*http.Response{
		sseResponse(http.StatusTooManyRequests, "limited"),
		sseResponse(http.StatusUnauthorized, "unauthorized"),
		sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[],\"usage\":{}}}\n\n"),
	}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if refresher.calls != 1 || len(provider.credentials) != 3 || provider.credentials[2].AccessToken != "new-token" {
		t.Fatalf("refresh calls=%d credentials=%#v", refresher.calls, provider.credentials)
	}
}

func TestUpstreamErrorStatusIsPreserved(t *testing.T) {
	server, _, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"bad request"}}`))}}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestModelsAreLimitedToAuthenticatedPoolAndScope(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	st.route.Pool.ModelAllowlist = []string{"pool-model"}
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+plain)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "pool-model") {
		t.Fatalf("models = %d %s", recorder.Code, recorder.Body.String())
	}
	st.route.Key.Scopes = []string{"responses"}
	request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+plain)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "insufficient_scope") {
		t.Fatalf("scope response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestInterruptedResponsesStreamDoesNotMeterOrBind(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &failingBody{data: []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-interrupted\"}}\n\n")}}}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","stream":true,"input":"hello"}`)
	if st.sessionSaved || st.usageInput != 0 || st.usageOutput != 0 {
		t.Fatalf("interrupted stream persisted state: %#v", st)
	}
	if strings.Contains(recorder.Body.String(), "response.completed") {
		t.Fatalf("unexpected completion: %s", recorder.Body.String())
	}
}

func TestIncompleteResponsesStreamMetersAndBinds(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp-incomplete\",\"usage\":{\"input_tokens\":9,\"output_tokens\":2}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","stream":true,"input":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !st.sessionSaved || st.usageInput != 9 || st.usageOutput != 2 {
		t.Fatalf("incomplete stream state: session=%v usage=%d/%d", st.sessionSaved, st.usageInput, st.usageOutput)
	}
}

func TestIncompleteChatStreamSendsLengthAndDone(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp-incomplete\",\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/chat/completions", `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if !strings.Contains(recorder.Body.String(), `"finish_reason":"length"`) || !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("chat stream = %s", recorder.Body.String())
	}
	if st.usageInput != 7 || st.usageOutput != 3 {
		t.Fatalf("usage = %d/%d", st.usageInput, st.usageOutput)
	}
}

func TestIncompleteChatJSONHasLengthFinishReason(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp-incomplete\",\"output\":[],\"usage\":{\"input_tokens\":6,\"output_tokens\":1}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/chat/completions", `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"finish_reason":"length"`) || st.usageInput != 6 || st.usageOutput != 1 || len(st.usageEvents) != 1 {
		t.Fatalf("chat response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestInterruptedChatStreamDoesNotSendDone(t *testing.T) {
	server, _, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: &failingBody{data: []byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")}}}
	recorder := serveGateway(t, server, plain, "/v1/chat/completions", `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("interrupted chat emitted DONE: %s", recorder.Body.String())
	}
}

func TestChatTranslationMapsMaxTokensAndTools(t *testing.T) {
	raw, err := chatToResponses([]byte(`{"model":"gpt-test","max_tokens":99,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	if value["max_output_tokens"] != float64(99) {
		t.Fatalf("max_output_tokens = %#v", value["max_output_tokens"])
	}
	tools := value["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tools = %#v", tools)
	}
}

func TestChatTranslationRejectsMissingFunction(t *testing.T) {
	if _, err := chatToResponses([]byte(`{"model":"gpt-test","messages":[],"tools":[{"type":"function"}]}`)); err == nil {
		t.Fatal("function tool without function was accepted")
	}
	server, _, provider, plain := newTestServer(t)
	recorder := serveGateway(t, server, plain, "/v1/chat/completions", `{"model":"gpt-test","messages":[],"tools":[{"type":"function"}]}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(provider.responses) != 0 {
		t.Fatal("provider was called")
	}
}

func TestDisabledMembershipReassignsNewSession(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.route.MembershipEnabled = false
	st.reassigned = accountWithCredentials(t, "account-2", "token-2")
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"usage\":{}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK || provider.credentials[0].AccessToken != "token-2" {
		t.Fatalf("response = %d %s credentials=%#v", recorder.Code, recorder.Body.String(), provider.credentials)
	}
}

func TestUsageAndSessionWritesRetry(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.usageFailures = 2
	st.sessionFailures = 2
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-retry\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","stream":true,"input":"hello"}`)
	if recorder.Code != http.StatusOK || st.usageCalls != 3 || st.sessionCalls != 3 || st.usageInput != 5 || !st.sessionSaved {
		t.Fatalf("response=%d usage calls=%d session calls=%d usage=%d/%d saved=%v", recorder.Code, st.usageCalls, st.sessionCalls, st.usageInput, st.usageOutput, st.sessionSaved)
	}
}

func TestUsageRetryDoesNotDuplicateCommittedEvent(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.usageCommitUncertain = true
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","stream":true,"input":"hello"}`)
	if recorder.Code != http.StatusOK || st.usageCalls != 2 || st.usageInput != 5 || st.usageOutput != 2 || len(st.usageEvents) != 1 {
		t.Fatalf("response=%d calls=%d usage=%d/%d events=%d", recorder.Code, st.usageCalls, st.usageInput, st.usageOutput, len(st.usageEvents))
	}
}

func TestResponseIDProducesStableUsageEventHash(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	fallback := server.randomUsageEventHash()
	first := server.usageEventHash("resp-stable", fallback)
	second := server.usageEventHash("resp-stable", server.randomUsageEventHash())
	if !bytes.Equal(first, second) || bytes.Contains(first, []byte("resp-stable")) {
		t.Fatalf("usage event hashes are not stable and irreversible")
	}
}

func TestTransientRefreshFailureCoolsAccount(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	server.refresher.(*fakeRefresher).err = errors.New("network unavailable")
	provider.responses = []*http.Response{sseResponse(http.StatusUnauthorized, "unauthorized")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusServiceUnavailable || len(st.status) == 0 || st.status[0] != domain.AccountCoolingDown {
		t.Fatalf("response=%d statuses=%#v body=%s", recorder.Code, st.status, recorder.Body.String())
	}
}

func TestDefinitiveRefreshFailureMarksAuthFailed(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	server.refresher.(*fakeRefresher).err = &codex.TokenError{StatusCode: http.StatusBadRequest, Code: "invalid_grant"}
	provider.responses = []*http.Response{sseResponse(http.StatusUnauthorized, "unauthorized")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusUnauthorized || len(st.status) == 0 || st.status[0] != domain.AccountAuthFailed {
		t.Fatalf("response=%d statuses=%#v body=%s", recorder.Code, st.status, recorder.Body.String())
	}
}

func newTestServer(t *testing.T) (*Server, *fakeStore, *fakeProvider, string) {
	t.Helper()
	cipher, err := credential.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.NewAPIKeys(bytes.Repeat([]byte{4}, 32))
	plain, _, _, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	account := accountWithCipher(t, cipher, "account-1", "old-token")
	st := &fakeStore{route: domain.KeyRoute{Key: domain.APIKey{ID: "key-1", PoolID: "pool-1"}, Pool: domain.Pool{ID: "pool-1", Provider: domain.ProviderCodex, ModelAllowlist: []string{"gpt-test"}}, Account: account, MembershipEnabled: true}, sessionAccount: account}
	provider := &fakeProvider{}
	refresher := &fakeRefresher{account: account}
	return New(st, keys, cipher, provider, refresher), st, provider, plain
}
func accountWithCredentials(t *testing.T, id, token string) domain.ProviderAccount {
	t.Helper()
	cipher, _ := credential.New(bytes.Repeat([]byte{3}, 32))
	return accountWithCipher(t, cipher, id, token)
}
func accountWithCipher(t *testing.T, cipher *credential.Cipher, id, token string) domain.ProviderAccount {
	t.Helper()
	raw, _ := json.Marshal(codex.Credentials{AccessToken: token, RefreshToken: "refresh", AccountID: "upstream-account"})
	encrypted, err := cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	return domain.ProviderAccount{ID: id, Provider: domain.ProviderCodex, Status: domain.AccountActive, CredentialCiphertext: encrypted, CredentialVersion: 1}
}
func sseResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}, "Retry-After": []string{"1"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func serveGateway(t *testing.T, server *Server, key, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

type failingBody struct {
	data []byte
	sent bool
}

func (b *failingBody) Read(target []byte) (int, error) {
	if b.sent {
		return 0, io.ErrUnexpectedEOF
	}
	b.sent = true
	return copy(target, b.data), io.ErrUnexpectedEOF
}
func (b *failingBody) Close() error { return nil }
