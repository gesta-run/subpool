package copilotstream

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestIdentityNormalizesInterleavedOutputSlots(t *testing.T) {
	var state Identity
	for _, event := range []map[string]any{
		{"output_index": json.Number("0"), "item": map[string]any{"id": "reason-start"}},
		{"output_index": json.Number("1"), "item": map[string]any{"id": "msg-start"}},
		{"output_index": json.Number("2"), "item": map[string]any{"id": "call-start", "call_id": "call-native"}},
	} {
		state.Normalize(event)
	}
	for _, test := range []struct {
		index int
		want  string
	}{{1, "msg-start"}, {0, "reason-start"}, {2, "call-start"}, {1, "msg-start"}} {
		event := map[string]any{"output_index": test.index, "item_id": "changing-delta-id", "delta": "illustrative"}
		state.Normalize(event)
		if event["item_id"] != test.want || event["delta"] != "illustrative" {
			t.Fatalf("delta was misrouted or changed: %#v", event)
		}
	}
	output := []any{
		map[string]any{"id": "reason-final", "encrypted_content": "illustrative-completed-reasoning"},
		map[string]any{"id": "msg-final", "phase": "final_answer", "content": []any{"illustrative"}},
		map[string]any{"id": "call-final", "call_id": "call-native", "arguments": "{}"},
	}
	response := map[string]any{"id": "resp-native-final", "output": output, "status": "completed"}
	state.Normalize(map[string]any{"type": "response.completed", "response": response})
	for index, want := range []string{"reason-start", "msg-start", "call-start"} {
		if output[index].(map[string]any)["id"] != want {
			t.Fatalf("terminal item identity changed: %#v", output[index])
		}
	}
	if response["id"] != "resp-native-final" || output[0].(map[string]any)["encrypted_content"] != "illustrative-completed-reasoning" ||
		output[1].(map[string]any)["phase"] != "final_answer" || output[2].(map[string]any)["call_id"] != "call-native" {
		t.Fatalf("upstream history fields changed: %#v", response)
	}
}

func TestIdentityPreservesStableEvents(t *testing.T) {
	var state Identity
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg-native","role":"assistant","content":[]}}`,
		`{"type":"response.output_text.delta","output_index":0,"item_id":"msg-native","delta":"Hi"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg-native","status":"completed"}}`,
		`{"type":"response.incomplete","response":{"id":"resp-native","status":"incomplete","output":[{"id":"msg-native","content":[]}]}}`,
		`{"type":"response.failed","response":{"id":"resp-native","status":"failed","error":{"code":"illustrative"}}}`,
	} {
		var event, expected map[string]any
		_ = json.Unmarshal([]byte(raw), &event)
		_ = json.Unmarshal([]byte(raw), &expected)
		state.Normalize(event)
		if !reflect.DeepEqual(event, expected) {
			t.Fatalf("stable event changed: got=%#v want=%#v", event, expected)
		}
	}
}

func TestIdentityRejectsAmbiguousOutputIndexes(t *testing.T) {
	var state Identity
	state.Normalize(map[string]any{"output_index": 0, "item_id": "msg-start"})
	for _, index := range []any{nil, "0", true, -1, -0.5, 0.5, math.NaN(), math.Inf(1), float64(1 << 63), json.Number("-1"), json.Number("0.5"), json.Number("9223372036854775808")} {
		event := map[string]any{"output_index": index, "item_id": "msg-unmapped", "item": map[string]any{"id": "msg-unmapped"}}
		state.Normalize(event)
		if event["item_id"] != "msg-unmapped" || event["item"].(map[string]any)["id"] != "msg-unmapped" {
			t.Fatalf("invalid index aliased to another item: %#v", event)
		}
	}
}

func TestIdentityPreservesSparseIndexPrecision(t *testing.T) {
	var state Identity
	for _, index := range []json.Number{"9007199254740992", "9007199254740993"} {
		state.Normalize(map[string]any{"output_index": index, "item_id": string(index)})
	}
	for _, index := range []json.Number{"9007199254740992", "9007199254740993"} {
		event := map[string]any{"output_index": index, "item_id": "changing-id"}
		state.Normalize(event)
		if event["item_id"] != string(index) {
			t.Fatalf("index precision was lost: %#v", event)
		}
	}
}

func TestIdentityLearnsLateIDsAndDoesNotCrossResponses(t *testing.T) {
	for _, id := range []string{"msg-first-response", "msg-next-response"} {
		var state Identity
		state.Normalize(map[string]any{"output_index": 0, "item": map[string]any{"id": ""}})
		state.Normalize(map[string]any{"output_index": 0, "item_id": id})
		item := map[string]any{"id": "msg-done"}
		state.Normalize(map[string]any{"output_index": 0, "item": item})
		if item["id"] != id {
			t.Fatalf("late or per-response identity was lost: %#v", item)
		}
	}
}
