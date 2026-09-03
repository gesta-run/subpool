package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"github.com/gesta-run/subpool/internal/store"
)

type fakeStore struct {
	store.Store
	route                   domain.KeyRoute
	sessionAccount          domain.ProviderAccount
	sessionErr              error
	reassigned              domain.ProviderAccount
	reassignedAccounts      []domain.ProviderAccount
	poolAccounts            []domain.ProviderAccount
	reassignExcludes        [][]string
	usageInput, usageOutput int64
	sessionSaved            bool
	savedAccount            string
	status                  []string
	usageFailures           int
	sessionFailures         int
	usageCalls              int
	sessionCalls            int
	allowCalls              int
	usageCommitUncertain    bool
	usageEvents             map[string]struct{}
}

func (f *fakeStore) ResolveAPIKey(context.Context, []byte) (domain.KeyRoute, error) {
	return f.route, nil
}
func (f *fakeStore) ResolvePinnedAPIKey(_ context.Context, _ []byte, poolID, accountID string) (domain.KeyRoute, error) {
	if f.route.Pool.ID != poolID || f.route.Account.ID != accountID {
		return domain.KeyRoute{}, store.ErrNotFound
	}
	return f.route, nil
}
func (f *fakeStore) ListPoolProviderAccounts(context.Context, string) ([]domain.ProviderAccount, error) {
	return f.poolAccounts, nil
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
func (f *fakeStore) ReassignAPIKey(_ context.Context, _, _ string, excludeIDs []string) (domain.ProviderAccount, error) {
	f.reassignExcludes = append(f.reassignExcludes, append([]string(nil), excludeIDs...))
	if len(f.reassignedAccounts) > 0 {
		account := f.reassignedAccounts[0]
		f.reassignedAccounts = f.reassignedAccounts[1:]
		return account, nil
	}
	if f.reassigned.ID == "" {
		return domain.ProviderAccount{}, store.ErrNoEligibleAccount
	}
	return f.reassigned, nil
}
func (f *fakeStore) AllowAPIKeyRequest(context.Context, string, int, time.Time) (bool, error) {
	f.allowCalls++
	return true, nil
}
func (f *fakeStore) RecordRequestSuccess(context.Context, string, string, time.Time) error {
	return nil
}
func (f *fakeStore) AddUsage(_ context.Context, _ string, eventHash []byte, _ string, _ time.Time, input, output int64) error {
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
func (f *fakeStore) RecordProviderHealthFailure(context.Context, string, string, time.Time, time.Time) error {
	return nil
}

type fakeProvider struct {
	responses   []*http.Response
	errors      []error
	models      []codex.Model
	credentials []codex.Credentials
	bodies      [][]byte
	headers     []http.Header
}

type fakeCompatibleProvider struct {
	chatBody      []byte
	responsesBody []byte
	credentials   openaicompat.Credentials
}

func (f *fakeCompatibleProvider) Models(context.Context, openaicompat.Credentials) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"compatible-model"}]}`))}, nil
}

func (f *fakeCompatibleProvider) Responses(_ context.Context, body []byte, _ http.Header, credentials openaicompat.Credentials) (*http.Response, error) {
	f.responsesBody = append([]byte(nil), body...)
	f.credentials = credentials
	return sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-api\",\"output\":[],\"usage\":{}}}\n\n"), nil
}

func (f *fakeCompatibleProvider) ChatCompletions(_ context.Context, body []byte, _ http.Header, credentials openaicompat.Credentials) (*http.Response, error) {
	f.chatBody = append([]byte(nil), body...)
	f.credentials = credentials
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2,"total_tokens":11}}`))}, nil
}

func (f *fakeProvider) Responses(_ context.Context, body []byte, headers http.Header, credentials codex.Credentials) (*http.Response, error) {
	f.credentials = append(f.credentials, credentials)
	f.bodies = append(f.bodies, append([]byte(nil), body...))
	f.headers = append(f.headers, headers.Clone())
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return nil, err
		}
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func (f *fakeProvider) ListModels(context.Context, codex.Credentials) ([]codex.Model, error) {
	return f.models, nil
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
	wantInstallationID, _ := codex.InstallationID("account-1")
	metadata, _ := upstream["client_metadata"].(map[string]any)
	if provider.headers[0].Get("X-Codex-Installation-Id") != wantInstallationID || metadata["x-codex-installation-id"] != wantInstallationID {
		t.Fatalf("headers=%#v metadata=%#v", provider.headers[0], metadata)
	}
}

func TestOpenAICompatibleChatPassesThroughAndMetersUsage(t *testing.T) {
	cipher, err := credential.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	keys := auth.NewAPIKeys(bytes.Repeat([]byte{4}, 32))
	plain, _, _, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.example.com/v1", APIKey: "sk-test-placeholder"})
	encrypted, err := cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	account := domain.ProviderAccount{ID: "account-1", Provider: domain.ProviderOpenAICompatible, CredentialType: domain.CredentialAPIKey, Status: domain.AccountActive, CredentialCiphertext: encrypted, CredentialVersion: 1}
	st := &fakeStore{route: domain.KeyRoute{Key: domain.APIKey{ID: "key-1", PoolID: "pool-1"}, Pool: domain.Pool{ID: "pool-1", Provider: domain.ProviderOpenAICompatible}, Account: account, MembershipEnabled: true}}
	compatible := &fakeCompatibleProvider{}
	server := New(st, keys, cipher, &fakeProvider{}, &fakeRefresher{}, compatible)
	body := `{"model":"compatible-model","messages":[{"role":"user","content":"hello"}]}`
	recorder := serveGateway(t, server, plain, "/v1/chat/completions", body)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"chat-1"`) {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if string(compatible.chatBody) != body || compatible.credentials.BaseURL != "https://api.example.com/v1" || compatible.credentials.APIKey != "sk-test-placeholder" {
		t.Fatalf("upstream request = %s, credentials=%#v", compatible.chatBody, compatible.credentials)
	}
	if st.usageInput != 9 || st.usageOutput != 2 {
		t.Fatalf("usage = %d/%d", st.usageInput, st.usageOutput)
	}
}

func TestNormalizeCodexRequestConvertsStringInput(t *testing.T) {
	body, err := normalizeCodexRequest([]byte(`{"model":"gpt-5.6-sol","input":"Reply with OK"}`), "11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Stream bool `json:"stream"`
		Store  bool `json:"store"`
		Input  []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if !request.Stream || request.Store || len(request.Input) != 1 || request.Input[0].Role != "user" || len(request.Input[0].Content) != 1 || request.Input[0].Content[0].Type != "input_text" || request.Input[0].Content[0].Text != "Reply with OK" {
		t.Fatalf("normalized request = %#v", request)
	}
}

func TestResponsesRejectsNonObjectClientMetadata(t *testing.T) {
	server, _, provider, plain := newTestServer(t)
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello","client_metadata":"invalid"}`)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_request_error") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(provider.credentials) != 0 {
		t.Fatal("provider was called")
	}
}

func TestResponsesJSONMetersTerminalResponse(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg-test\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg-test\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-json\",\"output\":[],\"usage\":{\"input_tokens\":8,\"output_tokens\":3}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK || st.usageInput != 8 || st.usageOutput != 3 || len(st.usageEvents) != 1 || !strings.Contains(recorder.Body.String(), `"text":"OK"`) {
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
	firstID, _ := codex.InstallationID("account-1")
	secondID, _ := codex.InstallationID("account-2")
	if len(provider.headers) != 2 || provider.headers[0].Get("X-Codex-Installation-Id") != firstID || provider.headers[1].Get("X-Codex-Installation-Id") != secondID || firstID == secondID {
		t.Fatalf("installation IDs were not rebound on failover")
	}
	for index, want := range []string{firstID, secondID} {
		var request map[string]any
		if err := json.Unmarshal(provider.bodies[index], &request); err != nil {
			t.Fatal(err)
		}
		metadata, _ := request["client_metadata"].(map[string]any)
		if metadata["x-codex-installation-id"] != want {
			t.Fatalf("attempt %d body installation ID = %v, want %q", index, metadata["x-codex-installation-id"], want)
		}
	}
	if len(st.status) == 0 || st.status[0] != domain.AccountCoolingDown {
		t.Fatalf("statuses = %#v", st.status)
	}
}

func TestMixedPoolFallsBackFromSubscriptionsToPaidAPI(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	st.route.Pool.Provider = domain.ProviderMixed
	st.reassignedAccounts = []domain.ProviderAccount{
		accountWithCipher(t, cipher, "account-2", "token-2"),
		compatibleAccountWithCipher(t, cipher, "account-api"),
	}
	provider.responses = []*http.Response{
		sseResponse(http.StatusTooManyRequests, "limited"),
		sseResponse(http.StatusTooManyRequests, "limited"),
	}
	compatible := &fakeCompatibleProvider{}
	server.compatible = compatible

	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(provider.credentials) != 2 || compatible.credentials.APIKey != "sk-paid-placeholder" {
		t.Fatalf("subscription calls=%d paid credentials=%#v", len(provider.credentials), compatible.credentials)
	}
	if len(st.reassignExcludes) != 2 || strings.Join(st.reassignExcludes[1], ",") != "account-1,account-2" {
		t.Fatalf("reassignment exclusions = %#v", st.reassignExcludes)
	}
}

func TestNewRequestReturnsFromPaidFallbackToSubscription(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	st.route.Pool.Provider = domain.ProviderMixed
	st.route.Account = compatibleAccountWithCipher(t, cipher, "account-api")
	st.reassignedAccounts = []domain.ProviderAccount{accountWithCipher(t, cipher, "account-subscription", "subscription-token")}
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-subscription\",\"output\":[],\"usage\":{}}}\n\n")}
	compatible := &fakeCompatibleProvider{}
	server.compatible = compatible

	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)

	if recorder.Code != http.StatusOK || len(provider.credentials) != 1 || provider.credentials[0].AccessToken != "subscription-token" {
		t.Fatalf("status=%d subscription credentials=%#v body=%s", recorder.Code, provider.credentials, recorder.Body.String())
	}
	if len(compatible.responsesBody) != 0 || len(st.reassignExcludes) != 1 || len(st.reassignExcludes[0]) != 0 {
		t.Fatalf("paid API called=%v reassignment exclusions=%#v", len(compatible.responsesBody) != 0, st.reassignExcludes)
	}
}

func TestProviderAttemptsAreBoundedWithoutReassigningPastLimit(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	for index := 2; index <= maxProviderAttempts+1; index++ {
		st.reassignedAccounts = append(st.reassignedAccounts, accountWithCredentials(t, fmt.Sprintf("account-%d", index), fmt.Sprintf("token-%d", index)))
	}
	for range maxProviderAttempts {
		provider.responses = append(provider.responses, sseResponse(http.StatusTooManyRequests, "limited"))
	}

	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)

	if recorder.Code != http.StatusTooManyRequests || len(provider.credentials) != maxProviderAttempts {
		t.Fatalf("status=%d provider attempts=%d body=%s", recorder.Code, len(provider.credentials), recorder.Body.String())
	}
	if len(st.reassignExcludes) != maxProviderAttempts-1 || len(st.reassignedAccounts) != 1 {
		t.Fatalf("reassignments=%d unconsumed accounts=%d", len(st.reassignExcludes), len(st.reassignedAccounts))
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

func TestModelsEndpointReturnsPoolModelsAndChecksScope(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	provider.models = []codex.Model{{Model: "gpt-test", DisplayName: "GPT Test"}}
	st.poolAccounts = []domain.ProviderAccount{st.route.Account}
	server.WithModelProviders(provider, &fakeCompatibleProvider{})
	mux := http.NewServeMux()
	server.Register(mux)
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+plain)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"gpt-test"`) || !strings.Contains(recorder.Body.String(), `"object":"model"`) {
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

func TestTransportErrorAndServerErrorReassignBeforeStreaming(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.reassignedAccounts = []domain.ProviderAccount{
		accountWithCredentials(t, "account-2", "second-token"),
		accountWithCredentials(t, "account-3", "third-token"),
	}
	provider.errors = []error{errors.New("connection reset"), nil, nil}
	provider.responses = []*http.Response{
		sseResponse(http.StatusBadGateway, "upstream unavailable"),
		sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-ok\",\"output\":[],\"usage\":{}}}\n\n"),
	}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(provider.credentials) != 3 || provider.credentials[2].AccessToken != "third-token" {
		t.Fatalf("credentials=%#v", provider.credentials)
	}
}

func TestUnhealthyBoundAccountIsReassigned(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.route.Account.HealthStatus = domain.HealthUnhealthy
	st.reassigned = accountWithCredentials(t, "account-2", "healthy-token")
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-ok\",\"output\":[],\"usage\":{}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK || len(provider.credentials) != 1 || provider.credentials[0].AccessToken != "healthy-token" {
		t.Fatalf("status=%d credentials=%#v body=%s", recorder.Code, provider.credentials, recorder.Body.String())
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
	raw, err := chatToResponses([]byte(`{"model":"gpt-test","temperature":0.2,"top_p":0.8,"reasoning_effort":"xhigh","max_tokens":99,"messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	if value["store"] != false || value["instructions"] != "" {
		t.Fatalf("Codex compatibility fields = %#v", value)
	}
	if _, exists := value["temperature"]; exists {
		t.Fatalf("temperature was forwarded: %#v", value)
	}
	if _, exists := value["top_p"]; exists {
		t.Fatalf("top_p was forwarded: %#v", value)
	}
	reasoning, _ := value["reasoning"].(map[string]any)
	if reasoning["effort"] != "xhigh" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
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
	st := &fakeStore{route: domain.KeyRoute{Key: domain.APIKey{ID: "key-1", PoolID: "pool-1"}, Pool: domain.Pool{ID: "pool-1", Provider: domain.ProviderCodex}, Account: account, MembershipEnabled: true}, sessionAccount: account}
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
func compatibleAccountWithCipher(t *testing.T, cipher *credential.Cipher, id string) domain.ProviderAccount {
	t.Helper()
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.example.com/v1", APIKey: "sk-paid-placeholder"})
	encrypted, err := cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	return domain.ProviderAccount{ID: id, Provider: domain.ProviderOpenAICompatible, CredentialType: domain.CredentialAPIKey, Status: domain.AccountActive, CredentialCiphertext: encrypted, CredentialVersion: 1}
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
