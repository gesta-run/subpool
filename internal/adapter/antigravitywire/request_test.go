package antigravitywire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRequestMapsMessagesControlsAndTools(t *testing.T) {
	raw, err := Request([]byte(`{"messages":[{"role":"developer","content":"Example instruction"},{"role":"user","content":[{"type":"text","text":"Hello"},{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}}}],"tool_choice":{"type":"function","function":{"name":"lookup"}},"reasoning_effort":"low","response_format":{"type":"json_object"},"max_completion_tokens":2048}`), "account-a", "gemini-3-flash", NewSignatures())
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	config := body["generationConfig"].(map[string]any)
	if config["maxOutputTokens"] != float64(2048) || config["responseMimeType"] != "application/json" || config["thinkingConfig"].(map[string]any)["thinkingLevel"] != "low" {
		t.Fatalf("bad controls: %s", raw)
	}
	if body["systemInstruction"] == nil || body["tools"] == nil || body["toolConfig"] == nil || !strings.Contains(string(raw), `"inlineData"`) || !strings.Contains(string(raw), `"additionalProperties":false`) {
		t.Fatalf("lost request semantics: %s", raw)
	}
}

func TestRequestPreservesSignedToolHistory(t *testing.T) {
	signatures := NewSignatures()
	signatures.Save("account-a", "gemini-3-flash", "example-call", "lookup", map[string]any{"id": "example"}, "example-signature")
	input := []byte(`{"messages":[{"role":"user","content":"Look up example"},{"role":"assistant","tool_calls":[{"id":"example-call","type":"function","function":{"name":"lookup","arguments":"{\"id\":\"example\"}"}}]},{"role":"tool","tool_call_id":"example-call","content":"example result"}]}`)
	raw, err := Request(input, "account-a", "gemini-3-flash", signatures)
	if err != nil || !strings.Contains(string(raw), `"thoughtSignature":"example-signature"`) || !strings.Contains(string(raw), `"functionResponse"`) {
		t.Fatalf("tool history not preserved: %s err=%v", raw, err)
	}
	for _, scope := range []string{"account-b", ""} {
		if _, err := Request(input, scope, "gemini-3-flash", signatures); err == nil {
			t.Fatal("signature crossed account boundary")
		}
	}
	changed := strings.Replace(string(input), `id\":\"example`, `id\":\"changed`, 1)
	if _, err := Request([]byte(changed), "account-a", "gemini-3-flash", signatures); err == nil {
		t.Fatal("modified arguments reused a signature")
	}
}

func TestRequestRejectsLossyConversions(t *testing.T) {
	tests := []string{
		`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]}]}`,
		`{"messages":[{"role":"tool","tool_call_id":"missing","content":"result"}]}`,
		`{"messages":[{"role":"assistant","tool_calls":[{"id":"missing","function":{"name":"lookup","arguments":"{}"}}]}]}`,
		`{"messages":[{"role":"user","content":"Hello"}],"tools":[{"type":"web_search"}]}`,
		`{"messages":[{"role":"user","content":"Hello"}],"n":2}`,
		`{"messages":[{"role":"user","content":"Hello"}],"logprobs":true}`,
	}
	for _, input := range tests {
		if _, err := Request([]byte(input), "account-a", "gemini-3-flash", NewSignatures()); err == nil {
			t.Errorf("unsupported request accepted: %s", input)
		}
	}
}

func TestSignaturesAndLimitsAreScopedAndBounded(t *testing.T) {
	s := NewSignatures()
	for range 4100 {
		s.Save("a", "m", newID(), "lookup", map[string]any{}, "signature")
	}
	if len(s.entries) > 4096 {
		t.Fatal("signature entry bound exceeded")
	}
	s.Save("a", "m", "expired", "lookup", map[string]any{}, "signature")
	entry := s.entries[signatureKey("a", "m", "expired")]
	entry.expires = time.Now().Add(-time.Minute)
	s.entries[signatureKey("a", "m", "expired")] = entry
	if _, ok := s.Lookup("a", "m", "expired", "lookup", map[string]any{}); ok {
		t.Fatal("expired signature reused")
	}
	limits := NewModelLimits()
	limits.Set("a", "gemini", time.Hour)
	if limits.Remaining("a", "gemini") <= 0 || limits.Remaining("a", "gemini") > 15*time.Minute || limits.Remaining("a", "claude") != 0 || limits.Remaining("b", "gemini") != 0 {
		t.Fatal("model cooldown was not scoped or bounded")
	}
}

func TestRequestEmitsToolConfigWithoutTools(t *testing.T) {
	for _, model := range []string{"gemini-3.1-pro-high", "claude-sonnet-4-5"} {
		for _, choice := range []string{"auto", "none"} {
			t.Run(model+"/"+choice, func(t *testing.T) {
				input := `{"messages":[{"role":"user","content":"Hello"}],"tool_choice":"` + choice + `"}`
				raw, err := Request([]byte(input), "a", model, NewSignatures())
				if err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				_ = json.Unmarshal(raw, &body)
				config, ok := body["toolConfig"].(map[string]any)
				if !ok {
					t.Fatalf("toolConfig missing: %s", raw)
				}
				mode := "VALIDATED"
				if choice == "none" {
					mode = "NONE"
				}
				if config["functionCallingConfig"].(map[string]any)["mode"] != mode {
					t.Fatalf("tool choice changed: %s", raw)
				}
			})
		}
	}
}

func TestClaudeThinkingRespectsOutputLimit(t *testing.T) {
	for _, tc := range []struct {
		name, controls string
		budget, limit  float64
		invalid        bool
	}{
		{name: "minimal", controls: `"reasoning_effort":"minimal","max_completion_tokens":2048`, budget: 1024, limit: 2048},
		{name: "high", controls: `"reasoning_effort":"high","max_completion_tokens":20000`, budget: 16384, limit: 20000},
		{name: "default limit", controls: `"reasoning_effort":"xhigh"`, budget: 24576, limit: 64000},
		{name: "completion limit precedence", controls: `"reasoning_effort":"high","max_tokens":2048,"max_completion_tokens":20000`, budget: 16384, limit: 20000},
		{name: "equal budget", controls: `"reasoning_effort":"low","max_completion_tokens":1024`, invalid: true},
		{name: "smaller limit", controls: `"reasoning_effort":"high","max_completion_tokens":2048`, invalid: true},
		{name: "invalid limit", controls: `"reasoning_effort":"low","max_completion_tokens":"2048"`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"messages":[{"role":"user","content":"Hello"}],` + tc.controls + `}`
			raw, err := Request([]byte(input), "a", "claude-sonnet-4-5", NewSignatures())
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), "output token limit greater than") {
					t.Fatalf("invalid budget was not rejected: %s %v", raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			config := body["generationConfig"].(map[string]any)
			if config["maxOutputTokens"] != tc.limit || config["thinkingConfig"].(map[string]any)["thinkingBudget"] != tc.budget {
				t.Fatalf("unexpected thinking controls: %s", raw)
			}
		})
	}
}

func TestAssistantHistoryPreservesGeminiPartOrder(t *testing.T) {
	input := `{"messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"First","tool_calls":[{"id":"first","function":{"name":"lookup","arguments":"{}"}}]},{"role":"assistant","content":"Second","tool_calls":[{"id":"second","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"first","content":"result"},{"role":"tool","tool_call_id":"second","content":"result"}]}`
	raw, err := Request([]byte(input), "a", "gemini-2.5-flash", NewSignatures())
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	parts := body["contents"].([]any)[1].(map[string]any)["parts"].([]any)
	if len(parts) != 4 || parts[0].(map[string]any)["text"] != "First" || parts[1].(map[string]any)["functionCall"] == nil || parts[2].(map[string]any)["text"] != "Second" || parts[3].(map[string]any)["functionCall"] == nil {
		t.Fatalf("Gemini history was reordered: %s", raw)
	}
}

func TestAssistantCannotInterruptPendingToolResults(t *testing.T) {
	input := `{"messages":[{"role":"assistant","tool_calls":[{"id":"first","function":{"name":"lookup","arguments":"{}"}},{"id":"second","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"first","content":"result"},{"role":"assistant","content":"Interrupted"},{"role":"tool","tool_call_id":"second","content":"result"}]}`
	if _, err := Request([]byte(input), "a", "gemini-2.5-flash", NewSignatures()); err == nil || !strings.Contains(err.Error(), "tool results must immediately follow") {
		t.Fatalf("incomplete tool-result turn was accepted: %v", err)
	}
}
