package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

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
