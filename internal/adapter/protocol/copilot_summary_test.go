package protocol

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCopilotSummaryRequiresCompleteText(t *testing.T) {
	message := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Fixture summary"}]}`
	for _, test := range []struct {
		name, status, item string
		valid              bool
	}{
		{"text", "completed", message, true},
		{"truncated", "incomplete", message, false},
		{"empty", "completed", `{"type":"message","role":"assistant","content":[]}`, false},
		{"refusal", "completed", `{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"fixture"}]}`, false},
		{"tool", "completed", `{"type":"function_call","call_id":"fixture","name":"read","arguments":"{}"}`, false},
		{"user", "completed", `{"type":"message","role":"user","content":[{"type":"output_text","text":"fixture"}]}`, false},
		{"opaque only", "completed", `{"type":"reasoning","encrypted_content":"fixture"}`, false},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", test.name, stream), func(t *testing.T) {
				body := fmt.Sprintf(`{"status":%q,"output":[%s],"usage":{"input_tokens":5,"output_tokens":2}}`, test.status, test.item)
				contentType := "application/json"
				if stream {
					body = fmt.Sprintf("data: {\"type\":\"response.%s\",\"response\":%s}\n\n", test.status, body)
					contentType = "text/event-stream"
				}
				response := &http.Response{Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
				text, input, output, err := ReadCopilotSummary(response, "responses")
				_ = response.Body.Close()
				if (err == nil) != test.valid || input != 5 || output != 2 || (test.valid && text != "Fixture summary") || (!test.valid && text != "") {
					t.Fatalf("text=%q usage=%d/%d error=%v", text, input, output, err)
				}
			})
		}
	}
}

func TestCopilotSummaryChatRequiresStop(t *testing.T) {
	for _, finish := range []string{"stop", "length", "content_filter", "tool_calls", "", "unknown"} {
		body := fmt.Sprintf(`{"choices":[{"finish_reason":%q,"message":{"role":"assistant","content":"Fixture summary"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, finish)
		response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
		text, input, output, err := ReadCopilotSummary(response, "chat_completions")
		_ = response.Body.Close()
		if (err == nil) != (finish == "stop") || input != 5 || output != 2 || (err != nil && text != "") {
			t.Fatalf("finish=%q text=%q usage=%d/%d error=%v", finish, text, input, output, err)
		}
	}
}

func TestCopilotSummaryRejectsOversizedStreamAndTrailingJSON(t *testing.T) {
	for _, test := range []struct{ contentType, body string }{
		{"application/json", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture"}]}]} trailing`},
		{"text/event-stream", "data: {}\n\n" + strings.Repeat(" ", 1<<20)},
	} {
		response := &http.Response{Header: http.Header{"Content-Type": {test.contentType}}, Body: io.NopCloser(strings.NewReader(test.body))}
		if text, _, _, err := ReadCopilotSummary(response, "responses"); err == nil || text != "" {
			t.Fatal("invalid or unbounded summary was accepted")
		}
		_ = response.Body.Close()
	}
}
