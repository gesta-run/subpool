package chatstream

import (
	"testing"
)

func toolChunk(t *testing.T, state *State, event map[string]any) map[string]any {
	t.Helper()
	delta, _, emit := state.Consume(event)
	if !emit {
		t.Fatalf("tool event not emitted: %v", event)
	}
	return delta["tool_calls"].([]any)[0].(map[string]any)
}

func TestParallelFunctionAndCustomCallsUseDenseStableIndices(t *testing.T) {
	state := new(State)
	for index, test := range []struct {
		output      int
		kind, field string
	}{
		{2, "function_call", "arguments"}, {5, "custom_tool_call", "input"},
	} {
		call := toolChunk(t, state, map[string]any{"type": "response.output_item.added", "output_index": test.output,
			"item": map[string]any{"type": test.kind, "call_id": "call_illustrative", "name": "illustrative", test.field: ""}})
		if call["index"] != index {
			t.Fatalf("tool index=%v expected=%d", call["index"], index)
		}
	}
	for _, test := range []struct {
		output, index            int
		event, kind, field, text string
	}{
		{5, 1, "response.custom_tool_call_input.delta", "custom", "input", "patch"},
		{2, 0, "response.function_call_arguments.delta", "function", "arguments", "{}"},
	} {
		call := toolChunk(t, state, map[string]any{"type": test.event, "output_index": test.output, "delta": test.text})
		if call["index"] != test.index || call[test.kind].(map[string]any)[test.field] != test.text {
			t.Fatalf("interleaved tool delta changed: %v", call)
		}
	}
	_, finish, emit := state.Consume(map[string]any{"type": "response.completed"})
	if !emit || !state.Terminal || finish != "tool_calls" {
		t.Fatalf("terminal=%v finish=%v", state.Terminal, finish)
	}
}

func TestToolDoneFlushesOnlyMissingSuffix(t *testing.T) {
	for _, test := range []struct{ kind, field, event string }{
		{"function_call", "arguments", "response.function_call_arguments.done"},
		{"custom_tool_call", "input", "response.custom_tool_call_input.done"},
	} {
		state := new(State)
		item := map[string]any{"type": test.kind, "call_id": "call_illustrative", "name": "illustrative", test.field: "prefix"}
		toolChunk(t, state, map[string]any{"type": "response.output_item.added", "output_index": 2, "item": item})
		call := toolChunk(t, state, map[string]any{"type": test.event, "output_index": 2, test.field: "prefix suffix"})
		kind := "function"
		if test.kind == "custom_tool_call" {
			kind = "custom"
		}
		if call[kind].(map[string]any)[test.field] != " suffix" {
			t.Fatalf("arguments duplicated: %v", call)
		}
		item[test.field] = "prefix suffix"
		_, _, emit := state.Consume(map[string]any{"type": "response.output_item.done", "output_index": 2, "item": item})
		if emit {
			t.Fatal("duplicate tool done emitted another delta")
		}
	}
}

func TestAddedToolWithOmittedArgumentsStartsWithEmptyString(t *testing.T) {
	call := toolChunk(t, new(State), map[string]any{"type": "response.output_item.added", "output_index": 0,
		"item": map[string]any{"type": "function_call", "call_id": "call_illustrative", "name": "illustrative"}})
	if call["function"].(map[string]any)["arguments"] != "" {
		t.Fatalf("initial arguments must not be null: %v", call)
	}
}
