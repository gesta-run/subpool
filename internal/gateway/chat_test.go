package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

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

func TestResponsesTranslationMapsMessagesAndTools(t *testing.T) {
	raw, copilotTools, err := responsesToChat([]byte(`{
		"model":"gpt-test","stream":true,"instructions":"Be concise","max_output_tokens":99,
		"reasoning":{"effort":"high"},
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call","call_id":"call-1","name":"lookup","arguments":"{\"id\":1}"},
			{"type":"function_call_output","call_id":"call-1","output":"result"}
		],
		"tools":[
			{"type":"function","name":"lookup","description":"Look up a record","parameters":{"type":"object"},"strict":true},
			{"type":"custom","name":"apply_patch","description":"Apply a patch","format":{"type":"text"}}
		],
		"tool_choice":{"type":"function","name":"lookup"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		t.Fatalf("translated request = %s", raw)
	}
	if value["max_tokens"] != float64(99) || value["reasoning_effort"] != "high" {
		t.Fatalf("translated request = %#v", value)
	}
	messages := value["messages"].([]any)
	if len(messages) != 4 || messages[0].(map[string]any)["role"] != "system" ||
		messages[2].(map[string]any)["tool_calls"] == nil || messages[3].(map[string]any)["tool_call_id"] != "call-1" {
		t.Fatalf("messages = %#v", messages)
	}
	tools := value["tools"].([]any)
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "lookup" || function["strict"] != true || len(copilotTools) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	customFunction := tools[1].(map[string]any)["function"].(map[string]any)
	if customFunction["name"] != "apply_patch" || customFunction["parameters"] == nil {
		t.Fatalf("custom tool = %#v", customFunction)
	}
}

func TestResponsesTranslationExpandsNamespacesAndSkipsToolSearch(t *testing.T) {
	raw, copilotTools, err := responsesToChat([]byte(`{
		"model":"gpt-test",
		"input":[
			{"type":"function_call","call_id":"call-1","name":"read_file","namespace":"workspace","arguments":"{\"path\":\"README.md\"}"},
			{"type":"function_call_output","call_id":"call-1","output":"contents"}
		],
		"tools":[
			{"type":"function","name":"workspace__read_file","parameters":{"type":"object"}},
			{"type":"namespace","name":"workspace","description":"Workspace tools","tools":[
				{"type":"function","name":"read_file","description":"Read a file","defer_loading":true,"parameters":{"type":"object"},"strict":true}
			]},
			{"type":"tool_search","execution":"server"}
		],
		"tool_choice":{"type":"function","name":"read_file","namespace":"workspace"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	tools := value["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %#v", tools)
	}
	namespaced := tools[1].(map[string]any)["function"].(map[string]any)
	upstreamName, _ := namespaced["name"].(string)
	info, ok := copilotTools[upstreamName]
	if !ok || info.Name != "read_file" || info.Namespace != "workspace" || upstreamName == "workspace__read_file" || len(upstreamName) > maxCopilotToolNameLength {
		t.Fatalf("namespaced tool = %#v mappings=%#v", namespaced, copilotTools)
	}
	if namespaced["defer_loading"] != nil || namespaced["strict"] != true {
		t.Fatalf("namespaced function = %#v", namespaced)
	}
	messages := value["messages"].([]any)
	call := messages[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["function"].(map[string]any)["name"] != upstreamName {
		t.Fatalf("messages = %#v", messages)
	}
	choice := value["tool_choice"].(map[string]any)["function"].(map[string]any)
	if choice["name"] != upstreamName {
		t.Fatalf("tool choice = %#v", choice)
	}
}

func TestResponsesTranslationRejectsUnknownToolSearchExecution(t *testing.T) {
	for _, execution := range []string{`"future"`, `1`} {
		_, _, err := responsesToChat([]byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"tool_search","execution":` + execution + `}]}`))
		if err == nil || !strings.Contains(err.Error(), "tool_search execution") {
			t.Fatalf("execution %s: error = %v", execution, err)
		}
	}
}

func TestResponsesTranslationEagerLoadsDeferredFunctionForClientToolSearch(t *testing.T) {
	raw, _, err := responsesToChat([]byte(`{"model":"gpt-test","input":"hello","tools":[{"type":"function","name":"lookup","defer_loading":true,"parameters":{"type":"object"}},{"type":"tool_search","execution":"client"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"name":"lookup"`) || strings.Contains(string(raw), "tool_search") || strings.Contains(string(raw), "defer_loading") {
		t.Fatalf("translated request = %s", raw)
	}
}

func TestChatCompletionTranslationMapsToolCalls(t *testing.T) {
	completion := map[string]any{
		"id": "chatcmpl-test", "model": "gpt-test", "created": float64(42),
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
				"id": "call-1", "function": map[string]any{"name": "lookup", "arguments": `{"id":1}`},
			}}},
		}},
		"usage": map[string]any{"prompt_tokens": float64(8), "completion_tokens": float64(3)},
	}
	response, input, output, err := chatCompletionToResponse(completion, "fallback", "fallback-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	items := response["output"].([]any)
	if response["id"] != "resp_test" || response["status"] != "completed" || input != 8 || output != 3 || len(items) != 1 {
		t.Fatalf("response = %#v usage=%d/%d", response, input, output)
	}
	call := items[0].(map[string]any)
	if call["type"] != "function_call" || call["call_id"] != "call-1" || call["name"] != "lookup" {
		t.Fatalf("function call = %#v", call)
	}
}

func TestChatCompletionTranslationRestoresCustomToolCall(t *testing.T) {
	completion := map[string]any{
		"id": "chatcmpl-custom",
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{"tool_calls": []any{map[string]any{
				"id": "call-1", "type": "function", "function": map[string]any{"name": "apply_patch", "arguments": `{"input":"*** Begin Patch"}`},
			}}},
		}},
	}
	response, _, _, err := chatCompletionToResponse(completion, "gpt-test", "fallback-id", map[string]copilotToolInfo{
		"apply_patch": {Name: "apply_patch", Custom: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := response["output"].([]any)[0].(map[string]any)
	if call["type"] != "custom_tool_call" || call["input"] != "*** Begin Patch" {
		t.Fatalf("custom call = %#v", call)
	}
}

func TestChatCompletionTranslationRestoresNamespace(t *testing.T) {
	completion := map[string]any{
		"id": "chatcmpl-namespace",
		"choices": []any{map[string]any{
			"finish_reason": "tool_calls",
			"message": map[string]any{"tool_calls": []any{map[string]any{
				"id": "call-1", "type": "function", "function": map[string]any{"name": "workspace__read_file", "arguments": `{"path":"README.md"}`},
			}}},
		}},
	}
	response, _, _, err := chatCompletionToResponse(completion, "gpt-test", "fallback-id", map[string]copilotToolInfo{
		"workspace__read_file": {Name: "read_file", Namespace: "workspace"},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := response["output"].([]any)[0].(map[string]any)
	if call["type"] != "function_call" || call["name"] != "read_file" || call["namespace"] != "workspace" {
		t.Fatalf("function call = %#v", call)
	}
}
