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
