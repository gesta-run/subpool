package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

func TestCopilotPayloadTooLargeIsNotRetried(t *testing.T) {
	for _, native := range []bool{true, false} {
		for _, path := range []string{"/v1/responses", "/v1/chat/completions"} {
			for _, stream := range []bool{true, false} {
				for _, pool := range []string{domain.ProviderCopilot, domain.ProviderMixed} {
					t.Run(fmt.Sprintf("native=%t/%s/stream=%t/pool=%s", native, path, stream, pool), func(t *testing.T) {
						server, st, _, plain := newTestServer(t)
						account := copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
						st.route.Account = account
						st.route.Pool.Provider = pool
						st.reassigned = account
						provider := &fakeCopilotProvider{
							supportedEndpoints: map[string]map[string]bool{"gpt-test": {
								copilot.EndpointResponses: native, copilot.EndpointChatCompletions: !native,
							}},
							response: &http.Response{StatusCode: http.StatusRequestEntityTooLarge,
								Header: http.Header{"Content-Type": {"text/plain"}, "Content-Encoding": {"gzip"}, "X-Request-Id": {"illustrative-upstream-id"}},
								Body:   io.NopCloser(strings.NewReader("failed to parse request: illustrative-private-error")),
							},
						}
						request := fmt.Sprintf(`{"model":"gpt-test","stream":%t,"input":"illustrative-input"}`, stream)
						if path == "/v1/chat/completions" {
							request = fmt.Sprintf(`{"model":"gpt-test","stream":%t,"messages":[{"role":"user","content":"illustrative-input"}]}`, stream)
						}
						recorder := serveGateway(t, server.WithCopilot(provider), plain, path, request)
						var envelope struct {
							Error struct{ Code, Type, Message string }
						}
						if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
							t.Fatal(err)
						}
						if recorder.Code != http.StatusRequestEntityTooLarge || envelope.Error.Code != "upstream_payload_too_large" || envelope.Error.Type != "invalid_request_error" ||
							!strings.Contains(envelope.Error.Message, "Shorten the conversation") || strings.Contains(recorder.Body.String(), "illustrative-private-error") {
							t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
						}
						if recorder.Header().Get("X-Subpool-Error-Source") != "upstream" || recorder.Header().Get("X-Request-Id") != "illustrative-upstream-id" || recorder.Header().Get("Content-Encoding") != "" {
							t.Fatalf("incorrect headers: %#v", recorder.Header())
						}
						if provider.chatCalls+provider.responsesCalls != 1 || len(st.reassignExcludes) != 0 || len(st.status) != 0 || len(st.healthFailureCodes) != 0 || st.usageCalls != 0 || st.sessionSaved {
							t.Fatal("payload rejection retried, penalized the account, or recorded a successful request")
						}
						if used := server.requestBodyBudget.used.Load(); used != 0 {
							t.Fatalf("request body reservation leaked: %d", used)
						}
					})
				}
			}
		}
	}
}

func TestCopilotLocalPayloadTooLargeNeverCallsUpstream(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{}
	server.WithRequestBodyLimits(16, 16*maxHTTPRequestBodyCopies, time.Minute)
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", `{"model":"gpt-test","input":"illustrative-input"}`)
	if recorder.Code != http.StatusRequestEntityTooLarge || recorder.Header().Get("X-Subpool-Error-Source") != "subpool" ||
		!strings.Contains(recorder.Body.String(), `"message":"request body is too large"`) || strings.Contains(recorder.Body.String(), "upstream_payload_too_large") {
		t.Fatalf("local rejection was not distinguished: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if provider.chatCalls+provider.responsesCalls != 0 {
		t.Fatal("a locally rejected payload was sent upstream")
	}
}
