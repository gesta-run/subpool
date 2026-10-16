package copilotcompact

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/gesta-run/subpool/internal/jsonobject"
)

// Features inspects only input item envelopes, never decoding image payloads.
func Features(raw []byte) (trigger, opaque, own bool) {
	input, _ := jsonobject.Lookup(raw, "input")
	_ = jsonobject.VisitArray(input, func(item []byte) error {
		switch itemString(item, "type") {
		case "compaction_trigger":
			trigger = true
		case "compaction":
			opaque = true
			// IDs also identify corrupted/missing tokens; never leak them upstream.
			own = own || strings.HasPrefix(itemString(item, "id"), "cmp_subpool_") || strings.HasPrefix(itemString(item, "encrypted_content"), statePrefix)
		}
		return nil
	})
	return
}

func Coverage(raw []byte) ([]string, error) {
	if len(raw) > MaxCalls*ChunkBytes {
		return nil, ErrUnsupported
	}
	var hashes []string
	input, _ := jsonobject.Lookup(raw, "input")
	err := jsonobject.VisitArray(input, func(item []byte) error {
		if itemString(item, "type") != "compaction_trigger" {
			hashes = append(hashes, fingerprint(item))
		}
		if len(hashes)*44 > MaxStateBytes/2 {
			return ErrUnsupported
		}
		return nil
	})
	return hashes, err
}

// Restore removes only authenticated, covered prefix items. Codex keeps user
// and instruction messages outside the opaque item; do not duplicate them or
// discard newly inserted instructions. Items after the marker are untouched.
func (state *State) Restore(raw []byte) ([]byte, error) {
	if len(raw) > MaxCalls*ChunkBytes {
		return nil, ErrUnsupported
	}
	object, err := jsonobject.Parse(raw)
	if err != nil || object.Duplicate("input") {
		return nil, ErrInvalidState
	}
	input, _ := object.Value("input")
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil {
		return nil, ErrInvalidState
	}
	var output []json.RawMessage
	restored := false
	for _, item := range items {
		if itemString(item, "type") != "compaction" {
			output = append(output, item)
			continue
		}
		if restored {
			return nil, ErrInvalidState
		}
		saved, err := state.open(item)
		if err != nil {
			return nil, err
		}
		// Uncovered prefix items are newer than the sealed snapshot. Preserve
		// their precedence and keep newly supplied results after retained calls.
		output = append(saved.Window, removeCovered(output, saved.Covered)...)
		restored = true
	}
	encoded, _ := json.Marshal(output)
	_ = object.Set("input", encoded)
	body := object.Bytes()
	if len(body) > MaxCalls*ChunkBytes {
		return nil, ErrUnsupported
	}
	return body, nil
}

// PrepareInband accepts exactly one final trigger and requires the full window.
// context_management is a different API contract, not an alias for a trigger.
func PrepareInband(raw []byte) (Plan, string, error) {
	if len(raw) > MaxCalls*ChunkBytes {
		return Plan{}, "", ErrUnsupported
	}
	object, err := jsonobject.Parse(raw)
	if err != nil || object.Duplicate("input") || itemString(raw, "previous_response_id") != "" {
		return Plan{}, "", ErrUnsupported
	}
	input, _ := object.Value("input")
	var items []json.RawMessage
	if json.Unmarshal(input, &items) != nil || len(items) < 2 {
		return Plan{}, "", ErrUnsupported
	}
	last := len(items) - 1
	trigger, err := jsonobject.Parse(items[last])
	if err != nil || itemString(items[last], "type") != "compaction_trigger" || !trigger.HasOnlyFields("type", "id") || trigger.Duplicate("id") || trigger.Duplicate("type") {
		return Plan{}, "", ErrUnsupported
	}
	id := itemString(items[last], "id")
	if value, ok := trigger.Value("id"); ok && string(value) != "null" && (len(id) > 256 || !bytes.HasPrefix(value, []byte(`"`))) {
		return Plan{}, "", ErrUnsupported
	}
	encoded, _ := json.Marshal(items[:last])
	_ = object.Set("input", encoded)
	plan, err := Prepare(object.Bytes())
	return plan, id, err
}
