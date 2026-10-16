package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

var errCopilotSummary = errors.New("Copilot did not return a complete text compaction summary")

// ReadCopilotSummary collects a bounded, completed, text-only inference result.
// Compaction must not install partial summaries, refusals, or tool calls. Usage
// remains observable for terminal results even when their summary is unusable.
func ReadCopilotSummary(response *http.Response, format string) (string, int64, int64, error) {
	raw, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return "", 0, 0, errCopilotSummary
	}
	var value any
	var input, output int64
	var status string
	if format == "chat_completions" {
		var completion map[string]any
		err = json.Unmarshal(raw, &completion)
		if err == nil {
			value, input, output, err = ChatCompletionToResponse(completion, "", "summary", nil)
			choices, _ := completion["choices"].([]any)
			if len(choices) == 1 {
				choice, _ := choices[0].(map[string]any)
				if choice["finish_reason"] == "stop" {
					status = "completed"
				}
			}
		}
	} else {
		if strings.Contains(response.Header.Get("Content-Type"), "application/json") && !json.Valid(raw) {
			return "", 0, 0, errCopilotSummary
		}
		copy := *response
		copy.Body = io.NopCloser(bytes.NewReader(raw))
		value, input, output, status, err = completedResponse(&copy)
	}
	result, _ := value.(map[string]any)
	if err != nil || status != "completed" || result["status"] != "completed" {
		return "", input, output, errCopilotSummary
	}
	items, _ := result["output"].([]any)
	var text strings.Builder
	for _, entry := range items {
		item, _ := entry.(map[string]any)
		if item["type"] == "reasoning" {
			continue
		}
		if item["type"] != "message" || item["role"] != "assistant" || (item["status"] != nil && item["status"] != "completed") {
			return "", input, output, errCopilotSummary
		}
		parts, _ := item["content"].([]any)
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			value, ok := part["text"].(string)
			if part["type"] != "output_text" || !ok {
				return "", input, output, errCopilotSummary
			}
			text.WriteString(value)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", input, output, errCopilotSummary
	}
	return text.String(), input, output, nil
}
