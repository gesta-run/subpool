package antigravitywire

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/adapter/protocol"
)

const exampleStream = "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"private thought\",\"thought\":true},{\"text\":\"Hello\"}]}}]}}\n\n" +
	"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"lookup\",\"args\":{\"id\":\"example\"}},\"thoughtSignature\":\"example-signature\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":2,\"cachedContentTokenCount\":4}}}\n\n"

func upstream(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestJSONResponseToolsSignaturesAndUsage(t *testing.T) {
	signatures := NewSignatures()
	response, err := Response(upstream(exampleStream), false, "account-a", "gemini-3-flash", signatures)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var completion map[string]any
	if err = json.NewDecoder(response.Body).Decode(&completion); err != nil {
		t.Fatal(err)
	}
	choice := completion["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if choice["finish_reason"] != "tool_calls" || message["content"] != "Hello" {
		t.Fatalf("bad response: %#v", completion)
	}
	usage := completion["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(12) || usage["completion_tokens"] != float64(5) {
		t.Fatalf("bad token accounting: %#v", usage)
	}
	call := message["tool_calls"].([]any)[0].(map[string]any)
	id := call["id"].(string)
	if signature, ok := signatures.Lookup("account-a", "gemini-3-flash", id, "lookup", map[string]any{"id": "example"}); !ok || signature != "example-signature" {
		t.Fatal("tool signature was lost")
	}
}

func TestStreamingResponseCompletesOnce(t *testing.T) {
	response, err := Response(upstream(exampleStream), true, "account-a", "gemini-3-flash", NewSignatures())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "[DONE]") != 1 || !strings.Contains(string(raw), `"finish_reason":"tool_calls"`) || !strings.Contains(string(raw), `"completion_tokens":5`) {
		t.Fatalf("invalid stream: %s", raw)
	}
	if strings.Contains(string(raw), "example-signature") {
		t.Fatal("Google signatures leaked to client text")
	}
}

func TestMalformedOrTruncatedStreamsFail(t *testing.T) {
	for _, raw := range []string{
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}}\n\n",
		"data: invalid\n\n",
		"data: {\"error\":{\"message\":\"example-private-detail\"}}\n\n",
		"data: {\"response\":{\"candidates\":[{\"finishReason\":\"MALFORMED_FUNCTION_CALL\"}]}}\n\n",
	} {
		if _, err := Response(upstream(raw), false, "a", "m", NewSignatures()); err == nil {
			t.Error("invalid stream treated as success")
		}
		response, err := Response(upstream(raw), true, "a", "m", NewSignatures())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if strings.Contains(string(body), "[DONE]") || !strings.Contains(string(body), `"error"`) || strings.Contains(string(body), "example-private-detail") {
			t.Fatalf("unsafe terminal stream: %s", body)
		}
	}
}

func TestBlockedAndNegativeUsage(t *testing.T) {
	response, err := Response(upstream("data: {\"response\":{\"promptFeedback\":{\"blockReason\":\"SAFETY\"},\"usageMetadata\":{\"promptTokenCount\":-3,\"candidatesTokenCount\":-1}}}\n\n"), false, "a", "m", NewSignatures())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(raw), `"finish_reason":"content_filter"`) || strings.Contains(string(raw), ":-") {
		t.Fatalf("bad blocked response: %s", raw)
	}
}

func TestToolArgumentsPreserveLargeIntegers(t *testing.T) {
	cache := NewSignatures()
	stream := "data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"example-call\",\"name\":\"lookup\",\"args\":{\"number\":9007199254740993}},\"thoughtSignature\":\"example-signature\"}]},\"finishReason\":\"STOP\"}]}}\n\n"
	response, err := Response(upstream(stream), false, "a", "gemini-3-flash", cache)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("tool argument was rounded: %s", raw)
	}
	request := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"example-call","type":"function","function":{"name":"lookup","arguments":"{\"number\":9007199254740993}"}}]},{"role":"tool","tool_call_id":"example-call","content":"example result"}]}`)
	raw, err = Request(request, "a", "gemini-3-flash", cache)
	if err != nil || !strings.Contains(string(raw), "9007199254740993") {
		t.Fatalf("signed arguments changed: %s %v", raw, err)
	}
}

func TestClaudeNativeHistoryPreservesSignaturesAndOrder(t *testing.T) {
	parts := []any{
		map[string]any{"thought": true, "text": "example thought", "thoughtSignature": "example-native-signature"},
		map[string]any{"text": "Checking first. "},
		map[string]any{"text": "", "thoughtSignature": "example-standalone-signature"},
		map[string]any{"functionCall": map[string]any{"id": "example-first", "name": "lookup", "args": map[string]any{}}},
		map[string]any{"text": "Checking second. "},
		map[string]any{"thought": true, "text": "another example thought", "thoughtSignature": "example-second-signature"},
		map[string]any{"functionCall": map[string]any{"id": "example-second", "name": "lookup", "args": map[string]any{}}},
		map[string]any{"text": "Waiting for results."},
	}
	for _, stream := range []bool{false, true} {
		for _, responses := range []bool{false, true} {
			name := map[bool]string{false: "chat", true: "responses"}[responses] + map[bool]string{false: "/json", true: "/stream"}[stream]
			t.Run(name, func(t *testing.T) {
				cache := NewSignatures()
				body := renderNativeHistory(t, parts, stream, responses, cache)
				raw, err := Request(body, "a", "claude-sonnet-4-5", cache)
				if err != nil {
					t.Fatal(err)
				}
				var request map[string]any
				_ = json.Unmarshal(raw, &request)
				actual := request["contents"].([]any)[1].(map[string]any)["parts"]
				actualJSON, _ := json.Marshal(actual)
				wantJSON, _ := json.Marshal(parts)
				if string(actualJSON) != string(wantJSON) {
					t.Fatalf("native history changed:\nactual: %s\nwant: %s", actualJSON, wantJSON)
				}
				changed := strings.Replace(string(body), "Checking first.", "Modified history.", 1)
				if _, err := Request([]byte(changed), "a", "claude-sonnet-4-5", cache); err == nil {
					t.Fatal("modified native history was accepted")
				}
			})
		}
	}
}

func renderNativeHistory(t *testing.T, parts []any, stream, responses bool, cache *Signatures) []byte {
	t.Helper()
	var source strings.Builder
	for i, part := range parts {
		candidate := map[string]any{"content": map[string]any{"parts": []any{part}}}
		if i == len(parts)-1 {
			candidate["finishReason"] = "STOP"
		}
		chunk, _ := json.Marshal(map[string]any{"response": map[string]any{"candidates": []any{candidate}}})
		source.WriteString("data: " + string(chunk) + "\n\n")
	}
	response, err := Response(upstream(source.String()), stream, "a", "claude-sonnet-4-5", cache)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return nativeHistoryRequest(t, response, stream, responses)
}

func nativeHistoryRequest(t *testing.T, response *http.Response, stream, responses bool) []byte {
	t.Helper()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var completion map[string]any
	if !stream {
		if err := json.Unmarshal(raw, &completion); err != nil {
			t.Fatal(err)
		}
	} else {
		var content strings.Builder
		var calls []any
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "data: {") {
				continue
			}
			var chunk map[string]any
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
				t.Fatal(err)
			}
			for _, value := range chunk["choices"].([]any) {
				delta := value.(map[string]any)["delta"].(map[string]any)
				if text, ok := delta["content"].(string); ok {
					content.WriteString(text)
				}
				if tools, ok := delta["tool_calls"].([]any); ok {
					calls = append(calls, tools...)
				}
			}
		}
		message := map[string]any{"role": "assistant", "content": content.String(), "tool_calls": calls}
		completion = map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "tool_calls"}}}
	}
	user := map[string]any{"role": "user", "content": "Hello"}
	if responses {
		converted, _, _, err := protocol.ChatCompletionToResponse(completion, "claude-sonnet-4-5", "example-response", nil)
		if err != nil {
			t.Fatal(err)
		}
		input := append([]any{user}, converted["output"].([]any)...)
		for _, id := range []string{"example-first", "example-second"} {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": id, "output": "example result"})
		}
		raw, _ := json.Marshal(map[string]any{"input": input})
		chat, _, err := protocol.ResponsesToChat(raw, false)
		if err != nil {
			t.Fatal(err)
		}
		return chat
	}
	message := completion["choices"].([]any)[0].(map[string]any)["message"]
	messages := []any{user, message}
	for _, id := range []string{"example-first", "example-second"} {
		messages = append(messages, map[string]any{"role": "tool", "tool_call_id": id, "content": "example result"})
	}
	body, _ := json.Marshal(map[string]any{"messages": messages})
	return body
}
