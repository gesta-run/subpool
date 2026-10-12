// Package chatstream converts Responses tool events to Chat Completions deltas.
package chatstream

import (
	"strings"

	"github.com/gesta-run/subpool/internal/protocol/responseevent"
)

// ToolCall preserves the discriminated function/custom Chat tool-call shape.
func ToolCall(item map[string]any) map[string]any {
	switch item["type"] {
	case "function_call":
		arguments, _ := item["arguments"].(string)
		return map[string]any{"id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": arguments}}
	case "custom_tool_call":
		input, _ := item["input"].(string)
		return map[string]any{"id": item["call_id"], "type": "custom", "custom": map[string]any{"name": item["name"], "input": input}}
	default:
		return nil
	}
}

type tool struct {
	index int
	kind  string
	field string
	text  strings.Builder
}

type State struct {
	tools    map[int]*tool
	Terminal bool
}

func (state *State) Consume(event map[string]any) (map[string]any, any, bool) {
	delta := make(map[string]any)
	var finish any
	switch event["type"] {
	case "response.output_item.added", "response.output_item.done":
		item, _ := event["item"].(map[string]any)
		call := ToolCall(item)
		if call == nil {
			return nil, nil, false
		}
		outputIndex := eventIndex(event["output_index"])
		if existing := state.tools[outputIndex]; existing != nil {
			return state.completeTool(existing, item)
		}
		if state.tools == nil {
			state.tools = make(map[int]*tool)
		}
		kind := call["type"].(string)
		field := "arguments"
		if kind == "custom" {
			field = "input"
		}
		text, _ := item[field].(string)
		entry := &tool{index: len(state.tools), kind: kind, field: field}
		entry.text.WriteString(text)
		state.tools[outputIndex] = entry
		call["index"] = entry.index
		delta["tool_calls"] = []any{call}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		entry := state.tools[eventIndex(event["output_index"])]
		if entry == nil {
			return nil, nil, false
		}
		text, _ := event["delta"].(string)
		entry.text.WriteString(text)
		return toolDelta(entry, text), nil, true
	case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
		entry := state.tools[eventIndex(event["output_index"])]
		if entry == nil {
			return nil, nil, false
		}
		return state.completeTool(entry, event)
	case "response.output_text.delta":
		delta["content"] = event["delta"]
	case "response.refusal.delta":
		delta["refusal"] = event["delta"]
	case "response.completed", "response.incomplete":
		state.Terminal = true
		finish = "stop"
		if len(state.tools) > 0 {
			finish = "tool_calls"
		}
		if event["type"] == "response.incomplete" {
			finish = "length"
		}
	default:
		return nil, nil, false
	}
	return delta, finish, true
}

func eventIndex(value any) int {
	if index, ok := value.(int); ok {
		return index
	}
	return int(responseevent.Number(value))
}

func (state *State) completeTool(entry *tool, item map[string]any) (map[string]any, any, bool) {
	text, _ := item[entry.field].(string)
	previous := entry.text.String()
	if text == previous || !strings.HasPrefix(text, previous) {
		return nil, nil, false
	}
	suffix := strings.TrimPrefix(text, previous)
	entry.text.WriteString(suffix)
	return toolDelta(entry, suffix), nil, true
}

func toolDelta(entry *tool, text string) map[string]any {
	return map[string]any{"tool_calls": []any{map[string]any{
		"index": entry.index, entry.kind: map[string]any{entry.field: text},
	}}}
}

func Usage(input, output int64) map[string]any {
	return map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}
}
