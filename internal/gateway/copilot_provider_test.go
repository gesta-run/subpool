package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

type fakeCopilotProvider struct {
	body               []byte
	credentials        copilot.Credentials
	err                error
	supportErr         error
	status             int
	response           *http.Response
	supportedEndpoints map[string]map[string]bool
	chatCalls          int
	responsesCalls     int
}

func (f *fakeCopilotProvider) ChatCompletions(_ context.Context, body []byte, _ http.Header, credentials copilot.Credentials) (*http.Response, error) {
	f.chatCalls++
	f.body = append([]byte(nil), body...)
	f.credentials = credentials
	if f.err != nil {
		return nil, f.err
	}
	if f.status != 0 {
		return &http.Response{StatusCode: f.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"model is not available"}}`))}, nil
	}
	if f.response != nil {
		return f.response, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"copilot-chat","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`))}, nil
}

func (f *fakeCopilotProvider) Responses(_ context.Context, body []byte, _ http.Header, credentials copilot.Credentials) (*http.Response, error) {
	f.responsesCalls++
	f.body = append([]byte(nil), body...)
	f.credentials = credentials
	if f.err != nil {
		return nil, f.err
	}
	if f.status != 0 {
		return &http.Response{StatusCode: f.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"model is not available"}}`))}, nil
	}
	if f.response != nil {
		return f.response, nil
	}
	return sseResponse(http.StatusOK, `data: {"type":"response.completed","response":{"id":"resp-copilot","model":"gpt-test","output":[],"usage":{"input_tokens":7,"output_tokens":3}}}`+"\n\n"), nil
}

func (f *fakeCopilotProvider) SupportsEndpoint(_ context.Context, model, endpoint string, _ copilot.Credentials) (bool, error) {
	if f.supportErr != nil {
		return false, f.supportErr
	}
	return f.supportedEndpoints[model][endpoint], nil
}

func TestCopilotResponsesUsesNativeEndpointWhenAdvertised(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{
		"gpt-test": {copilot.EndpointResponses: true},
	}}
	request := `{"model":"gpt-test","input":"hello","tools":[{"type":"web_search"}],"metadata":{"trace":"kept"}}`
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"id":"resp-copilot"`) {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	upstream := string(provider.body)
	if provider.responsesCalls != 1 || provider.chatCalls != 0 || !strings.Contains(upstream, `"type":"web_search"`) ||
		!strings.Contains(upstream, `"metadata":{"trace":"kept"}`) || !strings.Contains(upstream, `"stream":true`) {
		t.Fatalf("responses/chat calls = %d/%d, upstream=%s", provider.responsesCalls, provider.chatCalls, upstream)
	}
	if st.usageInput != 7 || st.usageOutput != 3 || !st.sessionSaved {
		t.Fatalf("usage=%d/%d session=%v", st.usageInput, st.usageOutput, st.sessionSaved)
	}
}

func TestCopilotChatUsesNativeResponsesForResponsesOnlyModel(t *testing.T) {
	tests := []struct {
		name     string
		request  string
		response string
		want     string
	}{
		{
			name:     "json",
			request:  `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`,
			response: `data: {"type":"response.completed","response":{"id":"resp-chat-native","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":7,"output_tokens":3}}}` + "\n\n",
			want:     `"object":"chat.completion"`,
		},
		{
			name:    "stream",
			request: `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hello"}]}`,
			response: `data: {"type":"response.created","response":{"id":"resp-chat-native","model":"gpt-test"}}` + "\n\n" +
				`data: {"type":"response.output_text.delta","item_id":"msg-chat-native","output_index":0,"content_index":0,"delta":"OK"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"id":"resp-chat-native","model":"gpt-test","output":[],"usage":{"input_tokens":7,"output_tokens":3}}}` + "\n\n",
			want: `"object":"chat.completion.chunk"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, st, _, plain := newTestServer(t)
			cipher := server.cipher.(*credential.Cipher)
			st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			provider := &fakeCopilotProvider{
				supportedEndpoints: map[string]map[string]bool{"gpt-test": {copilot.EndpointResponses: true}},
				response:           sseResponse(http.StatusOK, test.response),
			}
			recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/chat/completions", test.request)
			body := recorder.Body.String()
			if recorder.Code != http.StatusOK || provider.responsesCalls != 1 || provider.chatCalls != 0 ||
				!strings.Contains(body, test.want) || !strings.Contains(body, `"content":"OK"`) {
				t.Fatalf("status=%d responses/chat calls=%d/%d body=%s", recorder.Code, provider.responsesCalls, provider.chatCalls, body)
			}
			upstream := string(provider.body)
			if !strings.Contains(upstream, `"input":[{"content":"hello","role":"user"}]`) || !strings.Contains(upstream, `"stream":true`) {
				t.Fatalf("upstream request = %s", upstream)
			}
		})
	}
}

func TestCopilotNativeResponsesPreservesPreviousResponseID(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	account := copilotAccountWithCipher(t, cipher, "copilot-account")
	st.route.Account = account
	st.route.Pool.Provider = domain.ProviderCopilot
	st.sessionAccount = account
	provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{
		"gpt-test": {copilot.EndpointResponses: true},
	}}
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", `{"model":"gpt-test","previous_response_id":"resp-previous","input":"continue"}`)
	if recorder.Code != http.StatusOK || provider.responsesCalls != 1 || provider.chatCalls != 0 {
		t.Fatalf("status = %d, responses/chat calls=%d/%d, body=%s", recorder.Code, provider.responsesCalls, provider.chatCalls, recorder.Body.String())
	}
	if !strings.Contains(string(provider.body), `"previous_response_id":"resp-previous"`) {
		t.Fatalf("upstream request = %s", provider.body)
	}
}

func TestCopilotResponsesFailsOverWhenCapabilityDiscoveryFails(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	first := copilotAccountWithCipher(t, cipher, "copilot-account")
	st.route.Account = first
	st.route.Pool.Provider = domain.ProviderMixed
	st.reassigned = compatibleAccountWithCipher(t, cipher, "compatible-account")
	server.compatible = &fakeCompatibleProvider{}
	provider := &fakeCopilotProvider{supportErr: &copilot.HTTPError{
		StatusCode: http.StatusServiceUnavailable, Operation: "Copilot models",
	}}
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusOK || len(server.compatible.(*fakeCompatibleProvider).responsesBody) == 0 {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(st.healthFailureCodes) != 1 || st.healthFailureCodes[0] != "provider_5xx" {
		t.Fatalf("health failures = %#v", st.healthFailureCodes)
	}
}

func TestCopilotChatStreamMarksQuotaExhausted(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{response: sseResponse(http.StatusOK,
		"data: {\"error\":{\"message\":\"Copilot quota exhausted\",\"code\":\"copilot_quota_exhausted\"}}\n\n")}
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/chat/completions", `{"model":"gpt-test","stream":true,"messages":[{"role":"user","content":"hello"}]}`)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "copilot_quota_exhausted") {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(st.availabilityUpdates) != 1 || st.availabilityUpdates[0].accountID != "copilot-account" || st.availabilityUpdates[0].allowed {
		t.Fatalf("availability updates = %#v", st.availabilityUpdates)
	}
}

func TestCopilotResponsesWaitsForFragmentedCustomToolName(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{response: sseResponse(http.StatusOK,
		`data: {"id":"chatcmpl-tool","model":"gpt-test","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"apply_","arguments":""}}]},"finish_reason":null}]}`+"\n\n"+
			`data: {"id":"chatcmpl-tool","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"patch","arguments":"{\"input\":\"patch\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n"+
			"data: [DONE]\n\n")}
	request := `{"model":"gpt-test","stream":true,"input":"update it","tools":[{"type":"custom","name":"apply_patch","description":"Apply a patch"}]}`
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", request)
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || strings.Count(body, `"type":"response.output_item.added"`) != 1 ||
		!strings.Contains(body, `"type":"custom_tool_call"`) || strings.Contains(body, `"type":"function_call"`) {
		t.Fatalf("status = %d, body=%s", recorder.Code, body)
	}
}

func TestCopilotTokenRateLimitCoolsDownAccount(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	now := time.Date(2026, time.September, 28, 12, 0, 0, 0, time.UTC)
	server.now = func() time.Time { return now }
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "account-1")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{err: &copilot.HTTPError{
		StatusCode: http.StatusTooManyRequests, Operation: "Copilot token exchange", RetryAfter: "90",
	}}
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/chat/completions", `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`)
	if recorder.Code != http.StatusTooManyRequests || len(st.status) != 1 || st.status[0] != domain.AccountCoolingDown {
		t.Fatalf("status = %d, account statuses = %#v, body=%s", recorder.Code, st.status, recorder.Body.String())
	}
	want := now.Add(90 * time.Second)
	if st.cooldownUntil == nil || !st.cooldownUntil.Equal(want) || len(st.healthFailureCodes) != 0 {
		t.Fatalf("cooldown = %v, health failures = %#v", st.cooldownUntil, st.healthFailureCodes)
	}
}

func TestCopilotTokenServerErrorRecordsProviderFailure(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "account-1")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{err: &copilot.HTTPError{
		StatusCode: http.StatusServiceUnavailable, Operation: "Copilot token exchange",
	}}
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/chat/completions", `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if len(st.healthFailureCodes) != 1 || st.healthFailureCodes[0] != "provider_5xx" || len(st.status) != 0 {
		t.Fatalf("health failures = %#v, account statuses = %#v", st.healthFailureCodes, st.status)
	}
}
