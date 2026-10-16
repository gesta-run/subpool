package copilotcompact

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPrepareCompactsCompletedWorkWithinOneUserTurn(t *testing.T) {
	request := `{"model":"fixture","input":[
		{"role":"developer","content":"Keep illustrative constraints"},
		{"role":"user","content":"Fix the illustrative task"},
		{"type":"function_call","name":"read","call_id":"call_fixture","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_fixture","output":"large illustrative output"},
		{"role":"assistant","content":"Work completed"}
	]}`
	plan, err := Prepare([]byte(request))
	if err != nil || len(plan.Parts) == 0 || len(plan.Tail) != 0 {
		t.Fatalf("single-turn tool history cannot compact: %+v %v", plan, err)
	}
	window := Window(plan, "Illustrative work summary")
	if len(window) != 3 || itemString(window[0], "role") != "developer" || itemString(window[1], "content") != "Fix the illustrative task" {
		t.Fatalf("instructions/latest question changed: %s", window)
	}
	encoded, _ := json.Marshal(window)
	if strings.Contains(string(encoded), "function_call") || strings.Contains(string(encoded), "encrypted_content") {
		t.Fatalf("compaction fabricated opaque state or orphaned a tool result: %s", encoded)
	}
}

func TestPreparePreservesParallelPendingToolGroup(t *testing.T) {
	request := `{"model":"fixture","input":[{"role":"user","content":"fixture"},
		{"type":"function_call","name":"read","call_id":"call_a","arguments":"{}"},
		{"type":"custom_tool_call","name":"read","call_id":"call_b","input":"fixture"},
		{"type":"function_call_output","call_id":"call_a","output":"fixture result"}
	]}`
	plan, err := Prepare([]byte(request))
	if err != nil || len(plan.Tail) != 3 {
		t.Fatalf("parallel pending calls were separated: %v, %s", err, plan.Tail)
	}
	window := Window(plan, "summary")
	if !reflect.DeepEqual(window[len(window)-3:], plan.Tail) {
		t.Fatal("pending calls/results did not survive the replacement window")
	}
}

func TestPrepareRejectsUnknownOpaqueAndOrphanedItems(t *testing.T) {
	for _, item := range []string{
		`{"type":"compaction","encrypted_content":"illustrative-provider-state"}`,
		`{"type":"function_call_output","call_id":"missing","output":"fixture"}`,
		`{"role":"assistant","content":[{"type":"input_file","file_id":"fixture"}]}`,
		`{"type":"compaction_trigger"}`,
	} {
		if _, err := Prepare([]byte(`{"input":[{"role":"user","content":"fixture"},` + item + `]}`)); err == nil {
			t.Fatalf("unsafe item accepted: %s", item)
		}
	}
}

func TestPrepareKeepsImagesMultimodal(t *testing.T) {
	plan, err := Prepare([]byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,illustrative"}]},{"role":"assistant","content":"An illustrative image"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, part := range plan.Parts {
		if itemString(part, "type") == "input_image" {
			found = true
		} else if strings.Contains(string(part), "base64") {
			t.Fatal("image bytes were serialized into ordinary transcript text")
		}
	}
	if !found {
		t.Fatal("image was discarded")
	}
}

func TestPrepareNoHistoryNeedsNoInference(t *testing.T) {
	for _, input := range []string{`"fixture"`, `[{"role":"user","content":"fixture"}]`} {
		plan, err := Prepare([]byte(`{"input":` + input + `}`))
		if err != nil || len(plan.Parts) != 0 || len(Window(plan, "")) != 1 {
			t.Fatalf("no-op compaction changed the request or required inference: %+v %v", plan, err)
		}
	}
}

func TestPrepareRejectsV2AndOpaquePendingTail(t *testing.T) {
	for _, request := range []string{
		`{"context_management":[{"type":"compaction"}],"input":[{"role":"user","content":"fixture"}]}`,
		`{"input":[{"role":"user","content":"fixture"},{"type":"function_call","call_id":"fixture","name":"read"},{"type":"compaction_trigger"}]}`,
	} {
		if _, err := Prepare([]byte(request)); err == nil {
			t.Fatal("plaintext compaction claimed to support opaque state")
		}
	}
}

func TestRepeatedCompactionPreservesLatestRealUserRequest(t *testing.T) {
	const latest = "Keep exact illustrative constraint: do not deploy"
	raw := []byte(`{"input":[{"role":"user","content":"Illustrative initial task"},{"role":"assistant","content":"Earlier work"},{"role":"user","content":"` + latest + `"},{"role":"assistant","content":"More work"}]}`)
	for pass := 0; pass < 3; pass++ {
		plan, err := Prepare(raw)
		if err != nil || itemString(plan.Latest, "content") != latest || len(plan.Parts) == 0 {
			t.Fatalf("latest real request changed on pass %d: latest=%s error=%v", pass, plan.Latest, err)
		}
		window := Window(plan, "Illustrative rolling summary")
		if len(window) != 3 || itemString(window[1], "content") != latest || itemString(window[2], "role") != "assistant" {
			t.Fatalf("compaction retained stale summaries or lost the real request: %s", window)
		}
		var summary struct {
			Content []struct{ Type string }
		}
		if json.Unmarshal(window[2], &summary) != nil || len(summary.Content) != 1 || summary.Content[0].Type != "output_text" {
			t.Fatalf("summary is not a standard assistant message: %s", window[2])
		}
		window = append(window, json.RawMessage(`{"role":"assistant","content":"Continued work"}`))
		raw, _ = json.Marshal(map[string]any{"input": window})
	}
}

func TestCompactionRecognizesNewUserAfterSummary(t *testing.T) {
	plan, err := Prepare([]byte(`{"input":[{"role":"user","content":"Illustrative task"},{"role":"assistant","content":"Work completed"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	window := Window(plan, "Illustrative previous summary")
	window = append(window, json.RawMessage(`{"role":"user","content":"New illustrative request"}`))
	raw, _ := json.Marshal(map[string]any{"input": window})
	next, err := Prepare(raw)
	if err != nil || itemString(next.Latest, "content") != "New illustrative request" {
		t.Fatalf("new user request was not selected: latest=%s error=%v", next.Latest, err)
	}
	parts, _ := json.Marshal(next.Parts)
	if !strings.Contains(string(parts), "Illustrative previous summary") {
		t.Fatal("previous summary was not included in the next summarization")
	}
}
