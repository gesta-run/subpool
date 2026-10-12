package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gesta-run/subpool/internal/adapter/protocol/chatstream"
	"github.com/gesta-run/subpool/internal/protocol/responseevent"
	"io"
	"net/http"
	"strings"
)

func (s *Renderer) proxyChatJSON(w http.ResponseWriter, model string, resp *http.Response) {
	value, input, output, status, err := completedResponse(resp)
	if err != nil {
		var failure *responseEventError
		if errors.As(err, &failure) {
			s.hooks.Limit(failure.limit)
			writeResponseEventError(w, failure)
			return
		}
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	response, _ := value.(map[string]any)
	id, _ := response["id"].(string)
	created := s.hooks.Now().Unix()
	if createdValue, ok := response["created_at"].(float64); ok {
		created = int64(createdValue)
	}
	message := map[string]any{"role": "assistant", "content": ""}
	var text strings.Builder
	var refusal strings.Builder
	var toolCalls []any
	if outputItems, ok := response["output"].([]any); ok {
		for _, rawItem := range outputItems {
			item, _ := rawItem.(map[string]any)
			switch item["type"] {
			case "message":
				if contents, ok := item["content"].([]any); ok {
					for _, rawContent := range contents {
						content, _ := rawContent.(map[string]any)
						switch content["type"] {
						case "output_text":
							if part, ok := content["text"].(string); ok {
								text.WriteString(part)
							}
						case "refusal":
							if part, ok := content["refusal"].(string); ok {
								refusal.WriteString(part)
							}
						}
					}
				}
			case "function_call", "custom_tool_call":
				toolCalls = append(toolCalls, chatstream.ToolCall(item))
			}
		}
	}
	message["content"] = text.String()
	if refusal.Len() > 0 {
		message["refusal"] = refusal.String()
		if text.Len() == 0 {
			message["content"] = nil
		}
	}
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
	}
	finish := "stop"
	if status == "incomplete" {
		finish = "length"
	} else if len(toolCalls) > 0 {
		finish = "tool_calls"
	}
	result := map[string]any{"id": id, "object": "chat.completion", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": input + output}}
	if input > 0 || output > 0 {
		s.hooks.Usage(id, model, input, output)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Renderer) proxyChatStream(w http.ResponseWriter, model string, resp *http.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	id := ""
	created := s.hooks.Now().Unix()
	var input, output int64
	sentRole := false
	state := new(chatstream.State)
	failed := false
	limit := responseevent.LimitNone
	for scanner.Scan() {
		data := responseevent.SSEData(scanner.Bytes())
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		if limit == responseevent.LimitNone {
			limit = responseevent.ClassifyLimit(data)
		}
		if failure := responseFailure(data); failure != nil {
			writeSSE(w, map[string]any{"error": failure.payload})
			state.Terminal = true
			failed = true
			break
		}
		if eventID := responseevent.ResponseID(data); eventID != "" {
			id = eventID
		}
		i, o := responseevent.Usage(data)
		if i > input {
			input = i
		}
		if o > output {
			output = o
		}
		delta, finish, emit := state.Consume(event)
		if !emit {
			continue
		}
		if !sentRole {
			delta["role"] = "assistant"
			sentRole = true
		}
		chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if s.includeUsage {
			chunk["usage"] = nil
		}
		writeSSE(w, chunk)
	}
	s.hooks.Limit(limit)
	if scanner.Err() != nil || !state.Terminal {
		return
	}
	if s.includeUsage && !failed {
		writeSSE(w, map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{}, "usage": chatstream.Usage(input, output)})
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if !failed && (input > 0 || output > 0) {
		s.hooks.Usage(id, model, input, output)
	}
}

func (s *Renderer) proxyCompatibleChatJSON(w http.ResponseWriter, model string, resp *http.Response) {
	var value any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&value); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	raw, _ := json.Marshal(value)
	input, output := responseevent.Usage(raw)
	responseID := ""
	if payload, ok := value.(map[string]any); ok {
		responseID, _ = payload["id"].(string)
	}
	if input > 0 || output > 0 {
		s.hooks.Usage(responseID, model, input, output)
	}
	copyResponseHeaders(w.Header(), resp.Header)
	writeJSON(w, resp.StatusCode, value)
}

func (s *Renderer) proxyCompatibleChatStream(w http.ResponseWriter, model string, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(resp.StatusCode)
	responseID := ""
	limit := responseevent.LimitNone
	input, output, err := copySSE(w, resp.Body, func(data []byte) {
		if id := responseevent.ResponseID(data); id != "" {
			responseID = id
		}
		if limit == responseevent.LimitNone {
			limit = responseevent.ClassifyLimit(data)
		}
	})
	s.hooks.Limit(limit)
	if err == nil && (input > 0 || output > 0) {
		s.hooks.Usage(responseID, model, input, output)
	}
}

func writeSSE(w http.ResponseWriter, value any) {
	raw, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
