package protocol

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func auditObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func auditChatRequest(t *testing.T, raw string) map[string]any {
	t.Helper()
	converted, err := ChatToResponses([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return auditObject(t, converted)
}

func auditResponse(raw string, stream bool) *http.Response {
	contentType := "application/json"
	if stream {
		contentType = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(raw))}
}

func auditChunks(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var chunks []map[string]any
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		chunks = append(chunks, auditObject(t, []byte(strings.TrimPrefix(line, "data: "))))
	}
	return chunks
}

func TestResponsesChatTokenLimitIsProviderSpecific(t *testing.T) {
	for _, test := range []struct {
		name, field string
		convert     func([]byte, bool) ([]byte, map[string]ToolInfo, error)
	}{
		{"standard", "max_completion_tokens", ResponsesToChat},
		{"copilot", "max_tokens", ResponsesToCopilotChat},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, _, err := test.convert([]byte(`{"model":"illustrative-model","input":"hello","max_output_tokens":1024}`), false)
			if err != nil {
				t.Fatal(err)
			}
			chat := auditObject(t, body)
			if chat[test.field] != float64(1024) {
				t.Fatalf("token limit changed: %s", body)
			}
			delete(chat, test.field)
			if chat["max_tokens"] != nil || chat["max_completion_tokens"] != nil {
				t.Fatalf("conflicting token-limit fields: %s", body)
			}
		})
	}
}

func TestChatConversionRejectsNonDefaultUnrepresentableControls(t *testing.T) {
	for _, control := range []string{
		`"n":2`, `"stop":["ILLUSTRATIVE_END"]`, `"stop":"ILLUSTRATIVE_END"`,
		`"logit_bias":{"123":-100}`, `"frequency_penalty":0.5`, `"presence_penalty":0.5`,
		`"seed":42`, `"logprobs":true`, `"top_logprobs":2`,
		`"n":{}`, `"stop":{}`, `"logit_bias":[]`,
	} {
		t.Run(control, func(t *testing.T) {
			body, err := ChatToResponses([]byte(`{"messages":[{"role":"user","content":"hello"}],` + control + `}`))
			var unsupported *RequestError
			if body != nil || !errors.As(err, &unsupported) || unsupported.Code != "unsupported_parameter" {
				t.Fatalf("control silently discarded: body=%s error=%v", body, err)
			}
		})
	}
}

func TestChatConversionAcceptsDefaultUnrepresentableControls(t *testing.T) {
	for _, controls := range []string{
		`"n":1,"stop":[],"logit_bias":{},"frequency_penalty":0,"presence_penalty":0,"logprobs":false,"top_logprobs":0`,
		`"n":null,"stop":null,"logit_bias":null,"frequency_penalty":null,"presence_penalty":null,"seed":null,"logprobs":null,"top_logprobs":null`,
	} {
		if _, err := ChatToResponses([]byte(`{"messages":[{"role":"user","content":"hello"}],` + controls + `}`)); err != nil {
			t.Fatalf("default controls rejected: %v", err)
		}
	}
}

func TestProtocolChatFunctionControl(t *testing.T) {
	payload := auditChatRequest(t, `{"model":"illustrative-model","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":"OK"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{}}}}]}`)
	input := payload["input"].([]any)
	if input[0].(map[string]any)["type"] != "function_call" || input[1].(map[string]any)["call_id"] != "call_1" {
		t.Fatalf("function control failed: %v", input)
	}
}

func TestProtocolChatCustomDeclaration(t *testing.T) {
	payload := auditChatRequest(t, `{"model":"illustrative-model","messages":[{"role":"user","content":"Apply the patch"}],"tools":[{"type":"custom","custom":{"name":"apply_patch","description":"Apply a patch","format":{"type":"text"}}}]}`)
	tool := payload["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "custom" || tool["name"] != "apply_patch" || tool["custom"] != nil {
		t.Fatalf("custom declaration was not flattened into the Responses schema: %v", tool)
	}
}

func TestChatCustomGrammarUsesResponsesGrammarShape(t *testing.T) {
	payload := auditChatRequest(t, `{"model":"illustrative-model","messages":[{"role":"user","content":"Apply a patch"}],"tools":[{"type":"custom","custom":{"name":"apply_patch","format":{"type":"grammar","grammar":{"syntax":"regex","definition":"PATCH_[0-9]+"}}}}]}`)
	format := payload["tools"].([]any)[0].(map[string]any)["format"].(map[string]any)
	if format["grammar"] != nil || format["syntax"] != "regex" || format["definition"] != "PATCH_[0-9]+" {
		t.Fatalf("invalid Responses grammar: %v", format)
	}
}

func TestAssistantTextPrecedesItsToolCalls(t *testing.T) {
	payload := auditChatRequest(t, `{"model":"illustrative-model","messages":[{"role":"assistant","content":"Checking the fixture","tool_calls":[{"id":"call_illustrative","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]}`)
	input := payload["input"].([]any)
	if input[0].(map[string]any)["role"] != "assistant" || input[1].(map[string]any)["type"] != "function_call" {
		t.Fatalf("assistant message order changed: %v", input)
	}
}

func TestResponsesTextToolPartsRemainReadableText(t *testing.T) {
	body, _, err := ResponsesToChat([]byte(`{"model":"illustrative-model","input":[{"type":"function_call_output","call_id":"call_illustrative","output":[{"type":"input_text","text":"First "},{"type":"input_text","text":"second"}]}]}`), false)
	if err != nil {
		t.Fatal(err)
	}
	result := auditObject(t, body)["messages"].([]any)[0].(map[string]any)
	if result["content"] != "First second" {
		t.Fatalf("text parts were serialized instead of interpreted: %v", result)
	}
}

func TestProtocolChatCustomHistory(t *testing.T) {
	payload := auditChatRequest(t, `{"model":"illustrative-model","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"custom","custom":{"name":"apply_patch","input":"*** Begin Patch\n*** End Patch"}}]},{"role":"tool","tool_call_id":"call_1","content":"OK"}]}`)
	input := payload["input"].([]any)
	call := input[0].(map[string]any)
	output := input[1].(map[string]any)
	if call["type"] != "custom_tool_call" || call["name"] != "apply_patch" || call["input"] == nil || output["type"] != "custom_tool_call_output" {
		t.Fatalf("custom call/result types were lost: call=%v output=%v", call, output)
	}
}

func TestProtocolChatCustomChoice(t *testing.T) {
	_, err := ChatToResponses([]byte(`{"model":"illustrative-model","messages":[{"role":"user","content":"Apply the patch"}],"tools":[{"type":"custom","custom":{"name":"apply_patch","format":{"type":"text"}}}],"tool_choice":{"type":"custom","custom":{"name":"apply_patch"}}}`))
	if err != nil {
		t.Fatalf("valid named custom choice rejected: %v", err)
	}
}

func TestProtocolChatTextPartToolResult(t *testing.T) {
	payload := auditChatRequest(t, `{"model":"illustrative-model","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"OK"}]}]}`)
	input := payload["input"].([]any)
	result := input[1].(map[string]any)
	if text, ok := result["output"].(string); ok && text == "OK" {
		return
	}
	parts, ok := result["output"].([]any)
	if !ok || len(parts) != 1 || parts[0].(map[string]any)["type"] != "input_text" {
		t.Fatalf("Chat text part was not converted to a Responses tool output part: %v", result)
	}
}

func TestProtocolResponsesImageToolResult(t *testing.T) {
	raw := `{"model":"illustrative-model","tools":[{"type":"function","name":"capture","parameters":{"type":"object","properties":{}}}],"input":[{"type":"function_call","call_id":"call_1","name":"capture","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":[{"type":"input_image","image_url":"https://example.invalid/illustrative.png"}]}]}`
	converted, _, err := ResponsesToChat([]byte(raw), false)
	if err != nil {
		var unsupported *RequestError
		if !errors.As(err, &unsupported) || unsupported.Code != "unsupported_tool_output" {
			t.Fatalf("unexpected conversion error: %v", err)
		}
		return
	}
	chat := auditObject(t, converted)
	for _, rawMessage := range chat["messages"].([]any) {
		message := rawMessage.(map[string]any)
		if parts, ok := message["content"].([]any); ok {
			for _, rawPart := range parts {
				if rawPart.(map[string]any)["type"] == "image_url" {
					return
				}
			}
		}
	}
	t.Fatalf("image tool output accepted but reduced to text instead of vision content: %s", converted)
}

func TestProtocolResponsesCustomRendering(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			payload := `{"id":"resp_illustrative","status":"completed","output":[{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"apply_patch","input":"patch"}],"usage":{"input_tokens":7,"output_tokens":3}}`
			if stream {
				payload = `data: {"type":"response.created","response":{"id":"resp_illustrative"}}` + "\n\n" +
					`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ctc_1","call_id":"call_1","name":"apply_patch","input":""}}` + "\n\n" +
					`data: {"type":"response.custom_tool_call_input.delta","output_index":0,"item_id":"ctc_1","delta":"patch"}` + "\n\n" +
					`data: {"type":"response.completed","response":` + payload + "}\n\n"
			}
			recorder := httptest.NewRecorder()
			response := auditResponse(payload, stream)
			defer response.Body.Close()
			NewRenderer(Observer{}).Render(recorder, stream, "chat_completions", "responses", "illustrative-model", nil, response)
			if !strings.Contains(recorder.Body.String(), `"tool_calls"`) || !strings.Contains(recorder.Body.String(), `"type":"custom"`) || !strings.Contains(recorder.Body.String(), "patch") {
				t.Fatalf("custom call dropped from rendered Chat response: %s", recorder.Body.String())
			}
		})
	}
}

func TestProtocolResponsesDenseChatToolIndices(t *testing.T) {
	payload := `data: {"type":"response.created","response":{"id":"resp_illustrative"}}` + "\n\n" +
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}` + "\n\n" +
		`data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}` + "\n\n" +
		`data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Checking"}` + "\n\n" +
		`data: {"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}` + "\n\n" +
		`data: {"type":"response.function_call_arguments.delta","output_index":2,"item_id":"fc_1","delta":"{}"}` + "\n\n" +
		`data: {"type":"response.completed","response":{"id":"resp_illustrative","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3}}}` + "\n\n"
	recorder := httptest.NewRecorder()
	response := auditResponse(payload, true)
	defer response.Body.Close()
	NewRenderer(Observer{}).Render(recorder, true, "chat_completions", "responses", "illustrative-model", nil, response)
	seen := 0
	for _, chunk := range auditChunks(t, recorder.Body.String()) {
		for _, rawChoice := range chunk["choices"].([]any) {
			delta := rawChoice.(map[string]any)["delta"].(map[string]any)
			calls, _ := delta["tool_calls"].([]any)
			for _, rawCall := range calls {
				call := rawCall.(map[string]any)
				seen++
				if call["index"] != float64(0) {
					t.Errorf("first Chat tool index=%v, expected 0 independently of Responses output_index=2", call["index"])
				}
			}
		}
	}
	if seen != 2 {
		t.Fatalf("expected a tool declaration and argument delta, got %d", seen)
	}
}
