package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/jsonobject"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

const nativeReasoningNullItem = `{"type":"reasoning","id":"reason-test","summary":[],"content":null,"encrypted_content":"illustrative-opaque-data","status":"completed","example":9007199254740993,"extra":null}`

func TestCopilotNativeReasoningNullContentIsModelScoped(t *testing.T) {
	for _, model := range []string{"grok-4.6", "grok-4.7", "grok-4.6-fast", "grok-4.7-fast", "grok-test", "gpt-test", "gemini-test"} {
		t.Run(model, func(t *testing.T) {
			request := []byte(`{"input":[` + nativeReasoningNullItem + `],"reasoning":{"effort":"high"}}`)
			original := bytes.Clone(request)
			body, _, err := protocol.NormalizeCopilotNativeResponses(request, model, true)
			if err != nil {
				t.Fatal(err)
			}
			want := nativeReasoningNullItem
			if model == "grok-4.7" || model == "grok-4.6" {
				want = strings.Replace(want, `,"content":null`, "", 1)
			}
			input, _ := jsonobject.Lookup(body, "input")
			if string(input) != "["+want+"]" || !bytes.Equal(request, original) ||
				!bytes.Contains(body, []byte(`"reasoning":{"effort":"high"}`)) {
				t.Fatalf("unexpected history or request mutation: %s", body)
			}
		})
	}
}

func TestCopilotNativeReasoningPreservesOtherContent(t *testing.T) {
	for _, item := range []string{
		`{"type":"reasoning","summary":[],"encrypted_content":"illustrative"}`,
		`{"type":"reasoning","content":[],"summary":[]}`,
		`{"type":"reasoning","content":[{"type":"reasoning_text","text":"Illustrative reasoning"}],"summary":[]}`,
		`{"type":"reasoning","content":"null","summary":[]}`,
		`{"type":"message","role":"assistant","content":null}`,
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,illustrative"}],"example":9007199254740993}`,
	} {
		body, _, err := protocol.NormalizeCopilotNativeResponses([]byte(`{"input":[`+item+`]}`), "grok-4.7", true)
		input, _ := jsonobject.Lookup(body, "input")
		if err != nil || string(input) != "["+item+"]" {
			t.Fatalf("untouched history was changed: %s error=%v", body, err)
		}
	}
	request := `{"input":[{"type":"reas\u006fning","c\u006fntent": null,"summary":[],"encrypted_content":"illustrative"}]}`
	body, _, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-4.7", true)
	if err != nil || !bytes.Contains(body, []byte(`"type":"reas\u006fning"`)) || bytes.Contains(body, []byte(`c\u006fntent`)) {
		t.Fatalf("escaped reasoning fields were not normalized: %s error=%v", body, err)
	}
}

func TestCopilotNativeReasoningPreservesOtherProvidersAndFallback(t *testing.T) {
	for _, model := range []string{"grok-4.6", "grok-4.7"} {
		t.Run(model, func(t *testing.T) { testCopilotNativeReasoningFallback(t, model) })
	}
}

func testCopilotNativeReasoningFallback(t *testing.T, model string) {
	t.Helper()
	for _, providerKind := range []string{domain.ProviderCodex, domain.ProviderOpenAICompatible, domain.ProviderMixed} {
		t.Run(providerKind, func(t *testing.T) {
			server, st, codexProvider, plain := newTestServer(t)
			cipher := server.cipher.(*credential.Cipher)
			compatible := &fakeCompatibleProvider{}
			server.compatible = compatible
			copilotProvider := &fakeCopilotProvider{
				supportedEndpoints: map[string]map[string]bool{model: {copilot.EndpointResponses: true}},
				status:             http.StatusServiceUnavailable,
			}
			server.WithCopilot(copilotProvider)
			st.route.Pool.Provider = providerKind
			switch providerKind {
			case domain.ProviderCodex:
				codexProvider.responses = []*http.Response{sseResponse(http.StatusOK,
					`data: {"type":"response.completed","response":{"id":"resp-codex","output":[],"usage":{}}}`+"\n\n")}
			case domain.ProviderOpenAICompatible:
				st.route.Account = compatibleAccountWithCipher(t, cipher, "compatible-account")
			case domain.ProviderMixed:
				st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
				st.reassigned = compatibleAccountWithCipher(t, cipher, "compatible-account")
			}
			request := `{"model":"` + model + `","input":[` + nativeReasoningNullItem + `],"reasoning":{"effort":"high"}}`
			recorder := serveGateway(t, server, plain, "/v1/responses", request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			forwarded := compatible.responsesBody
			if providerKind == domain.ProviderCodex {
				forwarded = codexProvider.bodies[0]
			}
			if !bytes.Contains(forwarded, []byte(nativeReasoningNullItem)) ||
				!bytes.Contains(forwarded, []byte(`"reasoning":{"effort":"high"}`)) {
				t.Fatalf("original history was lost for %s: %s", providerKind, forwarded)
			}
			if providerKind == domain.ProviderMixed {
				assertNativeReasoningRequest(t, copilotProvider.body, []any{json.RawMessage(nativeReasoningNullItem)})
				if copilotProvider.responsesCalls != 1 {
					t.Fatalf("unexpected Copilot attempts: %d", copilotProvider.responsesCalls)
				}
			} else if copilotProvider.responsesCalls != 0 {
				t.Fatal("non-Copilot request used the Copilot normalizer")
			}
		})
	}
}

type nativeReasoningTestProvider struct {
	fakeCopilotProvider
	requests  chan []byte
	responses chan string
}

func (p *nativeReasoningTestProvider) Responses(_ context.Context, body []byte, _ http.Header, _ copilot.Credentials) (*http.Response, error) {
	p.requests <- bytes.Clone(body)
	return sseResponse(http.StatusOK, <-p.responses), nil
}

func TestCopilotNativeReasoningMultiTurnHTTPAndWebSocket(t *testing.T) {
	for _, model := range []string{"grok-4.6", "grok-4.7"} {
		t.Run(model, func(t *testing.T) { testCopilotNativeReasoningMultiTurn(t, model) })
	}
}

func testCopilotNativeReasoningMultiTurn(t *testing.T, model string) {
	t.Helper()
	for _, useWebSocket := range []bool{false, true} {
		t.Run(map[bool]string{false: "http", true: "websocket"}[useWebSocket], func(t *testing.T) {
			server, st, _, plain := newTestServer(t)
			st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			st.sessionSavedSignal = make(chan struct{}, 1)
			provider := &nativeReasoningTestProvider{
				fakeCopilotProvider: fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{
					model: {copilot.EndpointResponses: true},
				}},
				requests: make(chan []byte, 1), responses: make(chan string, 1),
			}
			server.WithCopilot(provider)
			exchange, cleanup := nativeReasoningTestExchange(t, server, plain, useWebSocket)
			defer cleanup()
			history := []any{map[string]any{"type": "message", "role": "user", "content": "Hello"}}
			for round := range 3 {
				if round == 1 {
					history = append(history, map[string]any{"type": "function_call_output", "call_id": "call-test", "output": "illustrative result"})
				} else if round == 2 {
					history = append(history, map[string]any{"type": "message", "role": "user", "content": "Continue"})
				}
				fixture, err := json.Marshal(map[string]any{"type": "response.completed", "response": map[string]any{
					"id": fmt.Sprintf("resp-test-%d", round), "model": model, "status": "completed",
					"output": nativeReasoningTestOutput(round), "usage": map[string]any{"input_tokens": 7, "output_tokens": 3},
				}})
				if err != nil {
					t.Fatal(err)
				}
				provider.responses <- "data: " + string(fixture) + "\n\n"
				request := map[string]any{"model": model, "input": history, "reasoning": map[string]any{"effort": "high"},
					"store": false, "include": []string{"reasoning.encrypted_content"},
					"tools": json.RawMessage(`[{"type":"namespace","name":"workspace","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}]`)}
				if useWebSocket {
					request["type"] = "response.create"
				}
				encoded, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				response := exchange(encoded)
				select {
				case <-st.sessionSavedSignal:
				case <-time.After(time.Second):
					t.Fatal("turn completion was not recorded")
				}
				assertNativeReasoningRequest(t, <-provider.requests, history)
				items := response["output"].([]any)
				if len(items) != 2 || items[0].(map[string]any)["encrypted_content"] != "illustrative-opaque-data" {
					t.Fatalf("reasoning was lost in round %d: %#v", round, response)
				}
				if round == 0 && (items[1].(map[string]any)["name"] != "lookup" || items[1].(map[string]any)["namespace"] != "workspace") {
					t.Fatalf("client-facing tool call was not restored: %#v", items[1])
				}
				items[0].(map[string]any)["content"] = nil // Simulate Codex CLI history serialization.
				history = append(history, items...)
			}
		})
	}
}

func nativeReasoningTestOutput(round int) []any {
	reasoning := map[string]any{"type": "reasoning", "id": fmt.Sprintf("reason-test-%d", round),
		"summary": []any{}, "encrypted_content": "illustrative-opaque-data", "status": "completed"}
	if round == 0 {
		return []any{reasoning, map[string]any{"type": "function_call", "id": "fc-test", "call_id": "call-test",
			"name": protocol.StableCopilotNativeToolName("workspace", "lookup"), "arguments": "{}", "status": "completed"}}
	}
	return []any{reasoning, map[string]any{"type": "message", "id": fmt.Sprintf("msg-test-%d", round), "role": "assistant",
		"content": []any{map[string]any{"type": "output_text", "text": "Illustrative reply", "annotations": []any{}}}}}
}

func nativeReasoningTestExchange(t *testing.T, server *Server, key string, useWebSocket bool) (func([]byte) map[string]any, func()) {
	t.Helper()
	if useWebSocket {
		server.WithResponsesWebSocket(true, false, "")
		client, cleanup := dialResponsesWSTestServer(t, server, key)
		return func(request []byte) map[string]any {
			writeResponsesWSMessage(t, client, string(request))
			event := readResponsesWSMessage(t, client)
			if event["type"] != "response.completed" {
				t.Fatalf("unexpected event: %#v", event)
			}
			return event["response"].(map[string]any)
		}, cleanup
	}
	return func(request []byte) map[string]any {
		recorder := serveGateway(t, server, key, "/v1/responses", string(request))
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}, func() {}
}

func assertNativeReasoningRequest(t *testing.T, body []byte, history []any) {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var expected []map[string]any
	if err := json.Unmarshal(encoded, &expected); err != nil {
		t.Fatal(err)
	}
	for _, item := range expected {
		if item["type"] == "reasoning" {
			if content, exists := item["content"]; exists && content == nil {
				delete(item, "content")
			}
		} else if item["type"] == "function_call" {
			name, _ := item["name"].(string)
			namespace, _ := item["namespace"].(string)
			item["name"] = protocol.NativeCopilotToolName(name, namespace)
			delete(item, "namespace")
		}
	}
	want, _ := json.Marshal(expected)
	got, _ := json.Marshal(request["input"])
	if !bytes.Equal(got, want) || request["stream"] != true ||
		!reflect.DeepEqual(request["reasoning"], map[string]any{"effort": "high"}) {
		t.Fatalf("unexpected outgoing request: %s; expected input=%s", body, want)
	}
}

func TestCopilotNativeReasoningDoesNotDecodeUntouchedHistory(t *testing.T) {
	const item = `{"type":"reasoning","summary":[],"content":[],"encrypted_content":"illustrative"}`
	var history bytes.Buffer
	history.WriteString(`{"input":[`)
	for index := range 40000 {
		if index > 0 {
			history.WriteByte(',')
		}
		history.WriteString(item)
	}
	history.WriteString(`]}`)
	for _, request := range [][]byte{nativeCopilotPlainHistory(), history.Bytes()} {
		allocations := testing.AllocsPerRun(1, func() {
			if _, _, err := protocol.NormalizeCopilotNativeResponses(request, "grok-4.7", true); err != nil {
				t.Fatal(err)
			}
		})
		if allocations > 128 {
			t.Fatalf("untouched history allocations scale with history length: %.0f", allocations)
		}
	}
}
