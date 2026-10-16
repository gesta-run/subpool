// Package copilotcompact implements bounded text-summary compaction and
// Subpool-owned opaque continuation state. It never fabricates upstream state
// or silently compacts ordinary inference input.
package copilotcompact

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gesta-run/subpool/internal/jsonobject"
)

const (
	ChunkBytes      = 128 << 10
	MaxImageBytes   = 2 << 20
	MaxSummaryBytes = 32 << 10
	MaxCalls        = 128
)

var ErrUnsupported = errors.New("compaction cannot safely summarize this input; reduce attachments or use client-side summarization")

type Plan struct {
	Model        string
	Instructions string
	Retained     []json.RawMessage
	Latest       json.RawMessage
	Tail         []json.RawMessage
	Parts        []json.RawMessage
}

// Prepare retains instructions, the latest user request and pending tool calls
// verbatim. Completed calls/results become quoted transcript data,
// not executable tools. Images remain real multimodal parts, never base64 text.
func Prepare(raw []byte) (Plan, error) {
	var request struct {
		Model        string          `json:"model"`
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
	}
	if len(raw) > MaxCalls*ChunkBytes {
		return Plan{}, ErrUnsupported
	}
	// Legacy requests cannot directly represent in-band compaction. The V2
	// entry point removes its validated final trigger before preparing a plan.
	for _, key := range []string{"context_management", "compaction_trigger"} {
		if value, exists := jsonobject.Lookup(raw, key); exists && string(value) != "null" {
			return Plan{}, ErrUnsupported
		}
	}
	if json.Unmarshal(raw, &request) != nil {
		return Plan{}, ErrUnsupported
	}
	items, err := inputItems(request.Input)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Model: request.Model, Instructions: request.Instructions}
	lastUser := -1
	for index, item := range items {
		if !supportedItem(item) {
			return Plan{}, ErrUnsupported
		}
		if itemString(item, "role") == "user" && (itemString(item, "type") == "" || itemString(item, "type") == "message") {
			lastUser = index
		}
	}
	if lastUser < 0 {
		return Plan{}, ErrUnsupported
	}
	tail, err := pendingTail(items)
	if err != nil {
		return Plan{}, err
	}
	plan.Tail = items[tail:]
	if lastUser < tail {
		plan.Latest = items[lastUser]
	}
	firstUser := true
	history := false
	for index, item := range items[:tail] {
		role, kind := itemString(item, "role"), itemString(item, "type")
		retained := role == "system" || role == "developer" || kind == "additional_tools" || (role == "user" && firstUser && index != lastUser && len(item) <= MaxSummaryBytes)
		if retained {
			plan.Retained = append(plan.Retained, item)
		}
		if role == "user" {
			firstUser = false
		}
		history = history || (!retained && index != lastUser)
		parts, err := transcriptParts(item)
		if err != nil {
			return Plan{}, err
		}
		plan.Parts = append(plan.Parts, parts...)
	}
	if !history {
		plan.Parts = nil // No inference is needed to return an unchanged short window.
	}
	return plan, nil
}

func inputItems(raw json.RawMessage) ([]json.RawMessage, error) {
	var text string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &text) == nil {
		message, _ := json.Marshal(map[string]string{"type": "message", "role": "user", "content": text})
		return []json.RawMessage{message}, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil, ErrUnsupported
	}
	return items, nil
}

func supportedItem(item json.RawMessage) bool {
	switch itemString(item, "type") {
	case "", "message":
		switch itemString(item, "role") {
		case "user", "assistant", "system", "developer":
			return true
		}
	case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output", "reasoning", "additional_tools":
		return true
	}
	return false
}

// Keep every unresolved call and its suffix together. A compaction requested
// between a tool call and its result must not orphan that result on continuation.
func pendingTail(items []json.RawMessage) (int, error) {
	pending := make(map[string]int)
	seen := make(map[string]bool)
	groupStart := len(items)
	for index, item := range items {
		kind, id := itemString(item, "type"), itemString(item, "call_id")
		switch kind {
		case "function_call", "custom_tool_call":
			if id == "" || seen[id] {
				return 0, ErrUnsupported
			}
			if len(pending) == 0 {
				groupStart = index
			}
			seen[id], pending[id] = true, index
		case "function_call_output", "custom_tool_call_output":
			if _, ok := pending[id]; !ok {
				return 0, ErrUnsupported
			}
			delete(pending, id)
			if len(pending) == 0 {
				groupStart = len(items)
			}
		}
	}
	return groupStart, nil
}

func itemString(item []byte, key string) string {
	raw, _ := jsonobject.Lookup(item, key)
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func textPart(text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]string{"type": "input_text", "text": text})
	return raw
}

func transcriptParts(raw json.RawMessage) ([]json.RawMessage, error) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil || item == nil {
		return nil, ErrUnsupported
	}
	kind := itemString(raw, "type")
	switch kind {
	case "additional_tools":
		return nil, nil // The exact inventory is retained, not summarized.
	case "", "message", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
	case "reasoning":
		// Opaque chain-of-thought is not text and cannot be decrypted by Subpool.
		delete(item, "encrypted_content")
	default:
		return nil, ErrUnsupported
	}
	var images []json.RawMessage
	for _, key := range []string{"content", "output"} {
		var parts []json.RawMessage
		if json.Unmarshal(item[key], &parts) != nil {
			continue
		}
		for index, part := range parts {
			switch itemString(part, "type") {
			case "input_text", "output_text", "text", "refusal":
			case "input_image":
				if len(part) > MaxImageBytes {
					return nil, ErrUnsupported
				}
				images = append(images, part)
				parts[index] = textPart("[Image supplied separately for this transcript item]")
			default:
				return nil, ErrUnsupported
			}
		}
		item[key], _ = json.Marshal(parts)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("invalid compaction transcript")
	}
	return append([]json.RawMessage{textPart(string(encoded))}, images...), nil
}
