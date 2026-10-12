package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func nativeCopilotToolResponseFixture() string {
	return strings.ReplaceAll(`event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp-native","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"reason-1","encrypted_content":"illustrative-opaque-data","summary":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"workspace__apply_patch","arguments":"","status":"in_progress"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc-1","output_index":1,"delta":"{\"input\":\"line one\\n"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc-1","output_index":1,"delta":"line two\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","sequence_number":5,"item_id":"fc-1","output_index":1,"arguments":"{\"input\":\"line one\\nline two\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":6,"output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"workspace__apply_patch","arguments":"{\"input\":\"line one\\nline two\"}","status":"completed"}}

event: response.completed
data: {"type":"response.completed","sequence_number":7,"response":{"id":"resp-native","status":"completed","model":"grok-test","output":[{"type":"reasoning","id":"reason-1","encrypted_content":"illustrative-opaque-data","summary":[]},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"workspace__apply_patch","arguments":"{\"input\":\"line one\\nline two\"}","status":"completed"}],"usage":{"input_tokens":7,"output_tokens":3}}}

	`, "workspace__apply_patch", StableCopilotNativeToolName("workspace", "apply_patch"))
}

func TestCopilotNativeResponsesCustomStreamWithoutArgumentsDone(t *testing.T) {
	fixture := nativeCopilotToolResponseFixture()
	start := strings.Index(fixture, "event: response.function_call_arguments.done")
	end := strings.Index(fixture[start:], "event: response.output_item.done") + start
	fixture = fixture[:start] + fixture[end:]
	state := &copilotNativeToolStream{mappings: map[string]ToolInfo{
		StableCopilotNativeToolName("workspace", "apply_patch"): {Name: "apply_patch", Namespace: "workspace", Custom: true},
	}, calls: make(map[string]*copilotNativeCall)}
	var output bytes.Buffer
	if err := state.writeStream(&output, strings.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "event: response.custom_tool_call_input.done") != 1 ||
		!strings.Contains(output.String(), `"input":"line one\nline two"`) {
		t.Fatalf("missing custom call completion: %s", output.String())
	}
}

func TestCopilotNativeResponsesRejectsMalformedStreamEvents(t *testing.T) {
	for _, event := range []string{"null", "[]", "invalid"} {
		state := &copilotNativeToolStream{mappings: map[string]ToolInfo{}, calls: map[string]*copilotNativeCall{}}
		if err := state.writeStream(io.Discard, strings.NewReader("data: "+event+"\n\n")); err == nil {
			t.Fatalf("malformed event was accepted: %q", event)
		}
	}
}

func TestCopilotNativeResponsesRestoresChangingCustomCallIDs(t *testing.T) {
	fixture := nativeCopilotToolResponseFixture()
	for _, kind := range []string{"response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "response.completed"} {
		lines := strings.Split(fixture, "\n")
		for index, line := range lines {
			if strings.Contains(line, `"type":"`+kind+`"`) {
				lines[index] = strings.ReplaceAll(line, `"fc-1"`, `"fc-changing-`+kind+`"`)
			}
		}
		fixture = strings.Join(lines, "\n")
	}
	state := &copilotNativeToolStream{mappings: map[string]ToolInfo{
		StableCopilotNativeToolName("workspace", "apply_patch"): {Name: "apply_patch", Namespace: "workspace", Custom: true},
	}, calls: make(map[string]*copilotNativeCall)}
	var output bytes.Buffer
	if err := state.writeStream(&output, strings.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "response.function_call_arguments.") || strings.Contains(output.String(), "fc-changing-") ||
		strings.Count(output.String(), "event: response.custom_tool_call_input.done") != 1 ||
		!strings.Contains(output.String(), `"input":"line one\nline two"`) || !strings.Contains(output.String(), `"call_id":"call-1"`) {
		t.Fatalf("custom call identity or protocol was lost: %s", output.String())
	}
}

func TestCopilotNativeResponsesStableContinuationAliases(t *testing.T) {
	tools := []any{map[string]any{"type": "namespace", "name": "workspace", "tools": []any{
		map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
	}}}
	first, _ := json.Marshal(map[string]any{"input": "Look up an illustrative record.", "tools": tools})
	_, before, err := NormalizeCopilotNativeResponses(first, "grok-test", true)
	if err != nil {
		t.Fatal(err)
	}
	alias := CopilotToolName("lookup", "workspace", before)
	tools = append(tools, map[string]any{"type": "function", "name": "workspace__lookup"})
	second, _ := json.Marshal(map[string]any{"previous_response_id": "resp-test", "input": "Continue.", "tools": tools})
	_, after, err := NormalizeCopilotNativeResponses(second, "grok-test", true)
	if err != nil || alias != CopilotToolName("lookup", "workspace", after) {
		t.Fatalf("native continuation alias changed: before=%#v after=%#v error=%v", before, after, err)
	}
	state := &copilotNativeToolStream{mappings: after, calls: make(map[string]*copilotNativeCall)}
	item := map[string]any{"type": "function_call", "name": alias, "arguments": "{}", "id": "fc-test"}
	if err := state.restoreItem(item); err != nil || item["name"] != "lookup" || item["namespace"] != "workspace" {
		t.Fatalf("previous alias was not restored: %#v error=%v", item, err)
	}
	continuation := []byte(`{"previous_response_id":"resp-test","input":[{"type":"function_call","name":"lookup","namespace":"workspace","arguments":"{}"}]}`)
	withoutTools, _, err := NormalizeCopilotNativeResponses(continuation, "grok-test", true)
	if err != nil || !bytes.Contains(withoutTools, []byte(alias)) {
		t.Fatalf("history without declarations changed alias: %s error=%v", withoutTools, err)
	}
}

func TestCopilotNativeResponsesPreservesIncompleteCustomInput(t *testing.T) {
	request := []byte(`{"input":"hello","tools":[{"type":"custom","name":"emit_token","format":{"type":"grammar","syntax":"regex","definition":"TEST_[0-9]+"}}]}`)
	_, mappings, err := NormalizeCopilotNativeResponses(request, "grok-test", true)
	if err != nil {
		t.Fatal(err)
	}
	state := &copilotNativeToolStream{mappings: mappings, calls: make(map[string]*copilotNativeCall)}
	item := map[string]any{"type": "function_call", "name": "emit_token", "id": "fc-test", "status": "incomplete", "arguments": `{"input":"TEST_"}`}
	if err := state.restoreItem(item); err != nil || item["type"] != "custom_tool_call" || item["input"] != "TEST_" {
		t.Fatalf("incomplete input was rejected: %#v error=%v", item, err)
	}
}
