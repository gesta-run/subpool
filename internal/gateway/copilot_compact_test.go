package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/adapter/copilotcompact"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

type compactCopilotProvider struct {
	fakeCopilotProvider
	requests        [][]byte
	successes       int
	maxBytes        int
	incomplete      bool
	incompleteAfter int
	failureAfter    int
}

func (p *compactCopilotProvider) Responses(_ context.Context, body []byte, _ http.Header, _ copilot.Credentials) (*http.Response, error) {
	p.responsesCalls++
	p.requests = append(p.requests, append([]byte(nil), body...))
	if rejected := p.reject(body); rejected != nil {
		return rejected, nil
	}
	p.successes++
	status := "completed"
	if p.incomplete || (p.incompleteAfter > 0 && p.successes > p.incompleteAfter) {
		status = "incomplete"
	}
	return sseResponse(200, fmt.Sprintf(`data: {"type":"response.%s","response":{"id":"resp_summary_fixture","status":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Illustrative recovered task summary"}]}],"usage":{"input_tokens":5,"output_tokens":2}}}`+"\n\n", status, status)), nil
}

func (p *compactCopilotProvider) ChatCompletions(_ context.Context, body []byte, _ http.Header, _ copilot.Credentials) (*http.Response, error) {
	p.chatCalls++
	p.requests = append(p.requests, append([]byte(nil), body...))
	if rejected := p.reject(body); rejected != nil {
		return rejected, nil
	}
	p.successes++
	finish := "stop"
	if p.incomplete || (p.incompleteAfter > 0 && p.successes > p.incompleteAfter) {
		finish = "length"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"id":"chat_summary_fixture","choices":[{"message":{"role":"assistant","content":"Illustrative recovered task summary"},"finish_reason":%q}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, finish)))}, nil
}

func (p *compactCopilotProvider) reject(body []byte) *http.Response {
	if p.failureAfter > 0 && p.successes >= p.failureAfter {
		return &http.Response{StatusCode: 503, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"provider_error"}}`))}
	}
	if p.maxBytes > 0 && len(body) > p.maxBytes {
		return &http.Response{StatusCode: 413, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"request_body_too_large","message":"illustrative private text"}}`))}
	}
	return nil
}

func TestCopilotCompactWindowCanContinue(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			server, st, _, key := newTestServer(t)
			st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			provider := &compactCopilotProvider{maxBytes: copilotcompact.ChunkBytes / 2}
			provider.supportedEndpoints = map[string]map[string]bool{"gpt-test": {copilot.EndpointResponses: native, copilot.EndpointChatCompletions: !native}}
			server.WithCopilot(provider)
			request := fmt.Sprintf(`{"model":"gpt-test","input":[{"role":"developer","content":"Keep fixture constraints"},{"role":"user","content":"Finish the fixture task"},{"type":"function_call","name":"read","call_id":"call_fixture","arguments":"{}"},{"type":"function_call_output","call_id":"call_fixture","output":%q}]}`, strings.Repeat("illustrative tool output ", 14000))
			recorder := serveGateway(t, server, key, "/v1/responses/compact", request)
			if recorder.Code != 200 || st.sessionSaved || st.usageCalls != 1 || st.usageInput != int64(provider.successes)*5 || st.usageOutput != int64(provider.successes)*2 {
				t.Fatalf("status=%d successes=%d usage=%d/%d calls=%d session=%v body=%s", recorder.Code, provider.successes, st.usageInput, st.usageOutput, st.usageCalls, st.sessionSaved, recorder.Body.String())
			}
			var compacted struct{ Output json.RawMessage }
			if err := json.Unmarshal(recorder.Body.Bytes(), &compacted); err != nil {
				t.Fatal(err)
			}
			if len(compacted.Output) >= len(request)/2 || strings.Contains(string(compacted.Output), "function_call_output") || strings.Contains(string(compacted.Output), "encrypted_content") {
				t.Fatal("the canonical window did not safely reduce completed history")
			}
			if provider.successes < 2 || len(provider.requests) <= provider.successes {
				t.Fatal("the test did not exercise multi-chunk compaction and capacity recovery")
			}
			for _, raw := range provider.requests {
				var upstream map[string]any
				_ = json.Unmarshal(raw, &upstream)
				if upstream["tools"] != nil || upstream["previous_response_id"] != nil {
					t.Fatal("a summary attempted tool execution or reused upstream continuation state")
				}
			}
			continued := serveGateway(t, server, key, "/v1/responses", fmt.Sprintf(`{"model":"gpt-test","input":%s}`, compacted.Output))
			if continued.Code != 200 || !st.sessionSaved {
				t.Fatalf("canonical output could not continue: %d %s", continued.Code, continued.Body.String())
			}
		})
	}
}

func TestCopilotIncompleteCompactNeverInstallsOrReplaysSummary(t *testing.T) {
	for _, native := range []bool{false, true} {
		server, st, _, key := newTestServer(t)
		st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
		st.route.Pool.Provider = domain.ProviderCopilot
		provider := &compactCopilotProvider{incomplete: true}
		provider.supportedEndpoints = map[string]map[string]bool{"gpt-test": {copilot.EndpointResponses: native, copilot.EndpointChatCompletions: !native}}
		recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses/compact", `{"model":"gpt-test","input":[{"role":"user","content":"fixture"},{"role":"assistant","content":"fixture history"}]}`)
		if recorder.Code != 502 || st.sessionSaved || st.usageCalls != 1 || st.usageInput != 5 || st.usageOutput != 2 || provider.chatCalls+provider.responsesCalls != 1 || strings.Contains(recorder.Body.String(), "Illustrative recovered") || len(st.healthFailureCodes) != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("incomplete summary became live state: status=%d session=%v usage=%d body=%s", recorder.Code, st.sessionSaved, st.usageCalls, recorder.Body.String())
		}
	}
}

func TestCopilotCompactFailureKeepsEarlierInferenceUsage(t *testing.T) {
	for _, native := range []bool{false, true} {
		server, st, _, key := newTestServer(t)
		st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
		st.route.Pool.Provider = domain.ProviderMixed
		provider := &compactCopilotProvider{incompleteAfter: 1}
		provider.supportedEndpoints = map[string]map[string]bool{"gpt-test": {copilot.EndpointResponses: native, copilot.EndpointChatCompletions: !native}}
		request := fmt.Sprintf(`{"model":"gpt-test","input":[{"role":"user","content":"fixture"},{"role":"assistant","content":%q}]}`, strings.Repeat("illustrative history ", 16000))
		recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses/compact", request)
		if recorder.Code != 502 || st.sessionSaved || st.usageCalls != 1 || st.usageInput != 10 || st.usageOutput != 4 || provider.chatCalls+provider.responsesCalls != 2 || len(st.reassignExcludes) != 0 || len(st.healthFailureCodes) != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("failed compact replayed or lost usage: status=%d usage=%d/%d calls=%d body=%s", recorder.Code, st.usageInput, st.usageOutput, provider.chatCalls+provider.responsesCalls, recorder.Body.String())
		}
	}
}

func TestCopilotCompactReservesAndReleasesTranscriptBuffers(t *testing.T) {
	server, st, _, _ := newTestServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-test","input":"fixture"}`))
	body, _, reservation, ok := server.readReservedRequest(httptest.NewRecorder(), request)
	if !ok {
		t.Fatal("valid compaction request was rejected")
	}
	defer reservation.release()
	if used := server.requestBodyBudget.used.Load(); used != int64(len(body))*responsesHTTPRequestBodyCopies {
		t.Fatalf("native admission reserved transcript buffers: %d", used)
	}
	// An accepted request keeps its limits even if settings change before routing.
	st.setSettings(domain.Settings{MaxRequestBodyBytes: 1, MaxInflightRequestBodyBytes: 4})
	ctx := context.WithValue(request.Context(), compactBodyReservationKey{}, reservation)
	release, err := reserveCompactionTranscript(ctx, len(body))
	if err != nil {
		t.Fatal(err)
	}
	if used := server.requestBodyBudget.used.Load(); used != int64(len(body))*compactHTTPRequestBodyCopies {
		t.Fatalf("transcript buffers were not reserved: %d", used)
	}
	release()
	if used := server.requestBodyBudget.used.Load(); used != int64(len(body))*responsesHTTPRequestBodyCopies {
		t.Fatalf("transcript reservation did not restore the original lease: %d", used)
	}
	reservation.release()
	if server.requestBodyBudget.used.Load() != 0 {
		t.Fatal("transcript buffers were not released")
	}
}

func TestCopilotCompactCapacityDoesNotCallOrPenalizeProvider(t *testing.T) {
	server, st, _, key := newTestServer(t)
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
	st.route.Pool.Provider = domain.ProviderMixed
	provider := &compactCopilotProvider{}
	body := `{"model":"gpt-test","input":"illustrative"}`
	server.WithRequestBodyLimits(int64(len(body)), int64(len(body))*maxHTTPRequestBodyCopies, time.Minute)
	recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses/compact", body)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "1" || recorder.Header().Get("X-Subpool-Error-Source") != "subpool" {
		t.Fatalf("local capacity error was not preserved: status=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if provider.chatCalls+provider.responsesCalls != 0 || len(st.reassignExcludes) != 0 || len(st.healthFailureCodes) != 0 || server.requestBodyBudget.used.Load() != 0 {
		t.Fatal("local capacity failure called or penalized the provider, or leaked its reservation")
	}
}

func TestCopilotInterruptedCompactDoesNotReplayCompletedInference(t *testing.T) {
	for _, native := range []bool{false, true} {
		server, st, _, key := newTestServer(t)
		st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
		st.route.Pool.Provider = domain.ProviderMixed
		provider := &compactCopilotProvider{failureAfter: 1}
		provider.supportedEndpoints = map[string]map[string]bool{"gpt-test": {copilot.EndpointResponses: native, copilot.EndpointChatCompletions: !native}}
		request := fmt.Sprintf(`{"model":"gpt-test","input":[{"role":"user","content":"fixture"},{"role":"assistant","content":%q}]}`, strings.Repeat("illustrative history ", 16000))
		recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses/compact", request)
		if recorder.Code != 502 || !strings.Contains(recorder.Body.String(), `"code":"compaction_interrupted"`) || st.sessionSaved || st.usageInput != 5 || st.usageOutput != 2 || provider.chatCalls+provider.responsesCalls != 2 || len(st.reassignExcludes) != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("partial compaction was replayed: status=%d usage=%d/%d calls=%d body=%s", recorder.Code, st.usageInput, st.usageOutput, provider.chatCalls+provider.responsesCalls, recorder.Body.String())
		}
	}
}
