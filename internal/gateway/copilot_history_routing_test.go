package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

func TestCopilotUnsupportedOptionsWebSocket(t *testing.T) {
	for _, test := range []struct{ model, option, code string }{
		{"claude-sonnet-5.5", `"tool_choice":"required"`, "unsupported_tool_choice"},
		{"gemini-3.8-flash", `"reasoning":{"effort":"xhigh"}`, "invalid_reasoning_effort"},
	} {
		t.Run(test.model, func(t *testing.T) {
			server, st, _, key := newTestServer(t)
			st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{test.model: {copilot.EndpointResponses: true}}}
			server.WithCopilot(provider).WithResponsesWebSocket(true, false, "")
			client, cleanup := dialResponsesWSTestServer(t, server, key)
			defer cleanup()
			writeResponsesWSMessage(t, client, `{"type":"response.create","model":"`+test.model+`","input":"ILLUSTRATIVE_PROMPT",`+test.option+`}`)
			event := readResponsesWSMessage(t, client)
			encoded, _ := json.Marshal(event)
			if event["type"] != "error" || !strings.Contains(string(encoded), `"code":"`+test.code+`"`) || provider.responsesCalls != 0 || provider.chatCalls != 0 {
				t.Fatalf("event=%s calls=%d/%d", encoded, provider.responsesCalls, provider.chatCalls)
			}
		})
	}
}

func TestCopilotAssistantHistoryWebSocketRoutes(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5.5", "claude-haiku-4.5"} {
		for _, native := range []bool{false, true} {
			t.Run(model+map[bool]string{false: "/chat", true: "/native"}[native], func(t *testing.T) {
				server, st, _, key := newTestServer(t)
				st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
				st.route.Pool.Provider = domain.ProviderCopilot
				st.sessionSavedSignal = make(chan struct{}, 1)
				endpoint := copilot.EndpointChatCompletions
				fixture := `data: {"id":"chat-test","choices":[{"delta":{"role":"assistant","content":"BRAVO_MARKER"}}]}` + "\n\n" +
					`data: {"id":"chat-test","choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
				if native {
					endpoint = copilot.EndpointResponses
					fixture = `data: {"type":"response.completed","response":{"id":"resp-test","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"BRAVO_MARKER"}]}],"usage":{}}}` + "\n\n"
				}
				provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{model: {endpoint: true}}, response: sseResponse(http.StatusOK, fixture)}
				server.WithCopilot(provider).WithResponsesWebSocket(true, false, "")
				client, cleanup := dialResponsesWSTestServer(t, server, key)
				defer cleanup()
				writeResponsesWSMessage(t, client, `{"type":"response.create","model":"`+model+`","input":[{"role":"user","content":"ALPHA_MARKER"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ALPHA_MARKER"}]},{"role":"user","content":"BRAVO_MARKER"}]}`)
				for range 30 {
					event := readResponsesWSMessage(t, client)
					if event["type"] == "error" || event["type"] == "response.failed" {
						t.Fatalf("unexpected WebSocket error: %#v", event)
					}
					if event["type"] == "response.completed" {
						select {
						case <-st.sessionSavedSignal:
						case <-time.After(time.Second):
							t.Fatal("turn completion was not recorded")
						}
						var request map[string]any
						if json.Unmarshal(provider.body, &request) != nil {
							t.Fatal("invalid outgoing request")
						}
						field := "messages"
						if native {
							field = "input"
						}
						messages := request[field].([]any)
						if len(messages) != 3 || messages[1].(map[string]any)["role"] != "assistant" || messages[1].(map[string]any)["content"] != "ALPHA_MARKER" {
							t.Fatalf("assistant history changed incorrectly: %s", provider.body)
						}
						return
					}
				}
				t.Fatal("no response.completed event")
			})
		}
	}
}

func TestCopilotNormalizedHistoryPreservesFallbackOriginal(t *testing.T) {
	for _, kind := range []string{"responses", "chat/completions"} {
		for _, native := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/chat", true: "/native"}[native], func(t *testing.T) {
				server, st, _, key := newTestServer(t)
				cipher := server.cipher.(*credential.Cipher)
				st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
				st.route.Pool.Provider = domain.ProviderMixed
				st.reassigned = compatibleAccountWithCipher(t, cipher, "compatible-account")
				compatible := &fakeCompatibleProvider{}
				server.compatible = compatible
				endpoint := copilot.EndpointChatCompletions
				if native {
					endpoint = copilot.EndpointResponses
				}
				provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{"claude-haiku-4.5": {endpoint: true}}, status: http.StatusServiceUnavailable}
				field := "input"
				if kind == "chat/completions" {
					field = "messages"
				}
				request := `{"model":"claude-haiku-4.5","stream":true,"` + field + `":[{"role":"assistant","content":[{"type":"text","text":"ILLUSTRATIVE_HISTORY"}]}]}`
				recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/"+kind, request)
				fallback := compatible.responsesBody
				if kind == "chat/completions" {
					fallback = compatible.chatBody
				}
				if recorder.Code != http.StatusOK || string(fallback) != request || !bytes.Contains(provider.body, []byte(`"content":"ILLUSTRATIVE_HISTORY"`)) {
					t.Fatalf("status=%d fallback=%s Copilot request=%s", recorder.Code, fallback, provider.body)
				}
			})
		}
	}
}

func TestCopilotRequestCompatibilityDoesNotAffectOtherProviders(t *testing.T) {
	for _, model := range []string{"claude-sonnet-5.5", "gemini-3.8-flash", "grok-4.6"} {
		t.Run(model, func(t *testing.T) {
			server, st, _, key := newTestServer(t)
			st.route.Account = compatibleAccountWithCipher(t, server.cipher.(*credential.Cipher), "compatible-account")
			st.route.Pool.Provider = domain.ProviderOpenAICompatible
			compatible := &fakeCompatibleProvider{}
			server.compatible = compatible
			request := `{"model":"` + model + `","stream":true,"tool_choice":"required","reasoning":{"effort":"xhigh"},"input":[{"role":"assistant","content":[{"type":"output_text","text":"OLD_DEMO"}]},` + nativeReasoningNullItem + `]}`
			recorder := serveGateway(t, server, key, "/v1/responses", request)
			if recorder.Code != http.StatusOK || string(compatible.responsesBody) != request {
				t.Fatalf("status=%d upstream=%s", recorder.Code, compatible.responsesBody)
			}
		})
	}
}
