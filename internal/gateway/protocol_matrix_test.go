package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/copilot"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

// Every value and payload in these fixtures is illustrative, not captured traffic.
type matrixCopilotClient struct {
	client      *openaicompat.Client
	credentials openaicompat.Credentials
	native      bool
}

func (client matrixCopilotClient) SupportsEndpoint(_ context.Context, _ string, endpoint string, _ copilot.Credentials) (bool, error) {
	return (endpoint == copilot.EndpointResponses) == client.native, nil
}

func (client matrixCopilotClient) Responses(ctx context.Context, body []byte, headers http.Header, _ copilot.Credentials) (*http.Response, error) {
	return client.client.Responses(ctx, body, headers, client.credentials)
}

func (client matrixCopilotClient) ChatCompletions(ctx context.Context, body []byte, headers http.Header, _ copilot.Credentials) (*http.Response, error) {
	return client.client.ChatCompletions(ctx, body, headers, client.credentials)
}

func TestProviderProtocolMatrixToolRoundTrip(t *testing.T) {
	for _, provider := range []string{domain.ProviderCodex, domain.ProviderOpenAICompatible, domain.ProviderCopilot} {
		for _, native := range []bool{true, false} {
			if provider == domain.ProviderCodex && !native {
				continue
			}
			for _, kind := range []string{"responses", "chat_completions"} {
				for _, stream := range []bool{false, true} {
					name := fmt.Sprintf("%s/native=%t/%s/stream=%t", provider, native, kind, stream)
					t.Run(name, func(t *testing.T) { matrixToolRoundTrip(t, provider, native, kind, stream) })
				}
			}
		}
	}
}

func matrixToolRoundTrip(t *testing.T, provider string, native bool, kind string, stream bool) {
	t.Helper()
	var turns atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := "/v1/chat/completions"
		if native {
			path = "/v1/responses"
		}
		if r.URL.Path != path {
			http.Error(w, "endpoint not found", http.StatusNotFound)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		turn := int(turns.Add(1))
		if turn == 2 {
			assertMatrixToolResult(t, request, native)
		}
		if native {
			matrixNativeReply(w, turn)
		} else {
			matrixChatReply(w, request, turn)
		}
	}))
	defer upstream.Close()
	server, st, _, key := newTestServer(t)
	configureMatrixProvider(t, server, st, upstream, provider, native)
	path := "/v1/responses"
	if kind == "chat_completions" {
		path = "/v1/chat/completions"
	}
	for turn := 1; turn <= 2; turn++ {
		recorder := serveGateway(t, server, key, path, matrixRequest(kind, stream, turn))
		if recorder.Code != http.StatusOK {
			t.Fatalf("turn=%d status=%d body=%s", turn, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if turn == 1 && (!strings.Contains(body, "lookup") || !strings.Contains(body, "call_illustrative")) {
			t.Fatalf("tool call lost: %s", body)
		}
		if turn == 2 && !strings.Contains(body, "Final answer") {
			t.Fatalf("final answer lost: %s", body)
		}
		if stream && kind == "chat_completions" && (!strings.Contains(body, "[DONE]") || !strings.Contains(body, `"total_tokens":10`)) {
			t.Fatalf("Chat termination or requested usage lost: %s", body)
		}
		if stream && kind == "responses" && !strings.Contains(body, "response.completed") {
			t.Fatalf("Responses terminal event lost: %s", body)
		}
	}
	if turns.Load() != 2 || st.usageInput != 14 || st.usageOutput != 6 || len(st.usageEvents) != 2 {
		t.Fatalf("turns=%d usage=%d/%d events=%d", turns.Load(), st.usageInput, st.usageOutput, len(st.usageEvents))
	}
}

func configureMatrixProvider(t *testing.T, server *Server, st *fakeStore, upstream *httptest.Server, provider string, native bool) {
	t.Helper()
	st.route.Pool.Provider = provider
	cipher := server.cipher.(*credential.Cipher)
	credentials := openaicompat.Credentials{BaseURL: upstream.URL + "/v1", APIKey: "illustrative-key"}
	switch provider {
	case domain.ProviderCodex:
		server.codex = codex.NewClient(upstream.URL+"/v1", upstream.Client())
	case domain.ProviderCopilot:
		st.route.Account = copilotAccountWithCipher(t, cipher, "illustrative-copilot-account")
		server.copilot = matrixCopilotClient{client: openaicompat.NewClient(upstream.Client()), credentials: credentials, native: native}
	case domain.ProviderOpenAICompatible:
		st.route.Account = compatibleAccountWithCipher(t, cipher, "illustrative-compatible-account")
		raw, _ := json.Marshal(credentials)
		encrypted, err := cipher.Encrypt(raw)
		if err != nil {
			t.Fatal(err)
		}
		st.route.Account.CredentialCiphertext = encrypted
		server.compatible = openaicompat.NewClient(upstream.Client())
	}
}

func matrixRequest(kind string, stream bool, turn int) string {
	request := map[string]any{"model": "illustrative-model", "stream": stream}
	if kind == "chat_completions" {
		request["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}}}}
		messages := []any{map[string]any{"role": "user", "content": "Look up a fixture"}}
		if turn == 2 {
			messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "call_illustrative", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}},
				map[string]any{"role": "tool", "tool_call_id": "call_illustrative", "content": []any{map[string]any{"type": "text", "text": "Illustrative result"}}})
		}
		request["messages"] = messages
		if stream {
			request["stream_options"] = map[string]any{"include_usage": true}
		}
	} else {
		request["tools"] = []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}
		input := []any{map[string]any{"role": "user", "content": "Look up a fixture"}}
		if turn == 2 {
			input = append(input, map[string]any{"type": "function_call", "call_id": "call_illustrative", "name": "lookup", "arguments": "{}"},
				map[string]any{"type": "function_call_output", "call_id": "call_illustrative", "output": []any{map[string]any{"type": "input_text", "text": "Illustrative result"}}})
		}
		request["input"] = input
	}
	raw, _ := json.Marshal(request)
	return string(raw)
}

func assertMatrixToolResult(t *testing.T, request map[string]any, native bool) {
	t.Helper()
	field, resultType, idField, outputField := "messages", "tool", "tool_call_id", "content"
	if native {
		field, resultType, idField, outputField = "input", "function_call_output", "call_id", "output"
	}
	items := request[field].([]any)
	result := items[len(items)-1].(map[string]any)
	kind := result["role"]
	if native {
		kind = result["type"]
	}
	if kind != resultType || result[idField] != "call_illustrative" {
		t.Errorf("tool result identity changed: %v", result)
	}
	encoded, _ := json.Marshal(result[outputField])
	if !strings.Contains(string(encoded), "Illustrative result") {
		t.Errorf("tool result content lost: %s", encoded)
	}
	if native {
		if parts, ok := result["output"].([]any); ok && parts[0].(map[string]any)["type"] != "input_text" {
			t.Errorf("invalid Responses tool result parts: %v", parts)
		}
	}
}

func matrixNativeReply(w http.ResponseWriter, turn int) {
	w.Header().Set("Content-Type", "text/event-stream")
	id := fmt.Sprintf("resp_illustrative_%d", turn)
	response := map[string]any{"id": id, "status": "completed", "model": "illustrative-model", "usage": map[string]any{"input_tokens": 7, "output_tokens": 3}}
	writeMatrixEvent(w, map[string]any{"type": "response.created", "response": map[string]any{"id": id}})
	if turn == 1 {
		writeMatrixEvent(w, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs_illustrative", "type": "reasoning", "summary": []any{}}})
		item := map[string]any{"type": "function_call", "id": "fc_illustrative", "call_id": "call_illustrative", "name": "lookup", "arguments": ""}
		writeMatrixEvent(w, map[string]any{"type": "response.output_item.added", "output_index": 1, "item": item})
		writeMatrixEvent(w, map[string]any{"type": "response.function_call_arguments.delta", "output_index": 1, "item_id": "fc_illustrative", "delta": "{}"})
		item["arguments"] = "{}"
		item["status"] = "completed"
		response["output"] = []any{item}
		writeMatrixEvent(w, map[string]any{"type": "response.output_item.done", "output_index": 1, "item": item})
	} else {
		writeMatrixEvent(w, map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": "Final answer"})
		response["output"] = []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Final answer"}}}}
	}
	writeMatrixEvent(w, map[string]any{"type": "response.completed", "response": response})
}

func matrixChatReply(w http.ResponseWriter, request map[string]any, turn int) {
	message := map[string]any{"role": "assistant", "content": "Final answer"}
	finish := "stop"
	if turn == 1 {
		message["content"] = nil
		message["tool_calls"] = []any{map[string]any{"id": "call_illustrative", "type": "function", "function": map[string]any{"name": "lookup", "arguments": "{}"}}}
		finish = "tool_calls"
	}
	usage := map[string]any{"prompt_tokens": 7, "completion_tokens": 3, "total_tokens": 10}
	id := fmt.Sprintf("chatcmpl-illustrative-%d", turn)
	if stream, _ := request["stream"].(bool); !stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "model": "illustrative-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": usage})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if calls, ok := message["tool_calls"].([]any); ok {
		calls[0].(map[string]any)["index"] = 0
	}
	writeMatrixEvent(w, map[string]any{"id": id, "choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	writeMatrixEvent(w, map[string]any{"id": id, "choices": []any{}, "usage": usage})
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func writeMatrixEvent(w http.ResponseWriter, value any) {
	raw, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}
