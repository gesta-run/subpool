package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func chatToResponses(raw []byte) ([]byte, error) {
	var chat map[string]any
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("invalid JSON request")
	}
	response := make(map[string]any)
	for _, name := range []string{"model", "max_output_tokens", "reasoning", "tool_choice", "parallel_tool_calls"} {
		if value, ok := chat[name]; ok {
			response[name] = value
		}
	}
	if value, ok := chat["max_tokens"]; ok {
		response["max_output_tokens"] = value
	}
	if value, ok := chat["max_completion_tokens"]; ok {
		response["max_output_tokens"] = value
	}
	if effort, ok := chat["reasoning_effort"].(string); ok && strings.TrimSpace(effort) != "" {
		response["reasoning"] = map[string]any{"effort": effort}
	}
	response["stream"] = true
	response["store"] = false
	response["instructions"] = ""
	messages, ok := chat["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("messages is required")
	}
	var input []any
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role == "tool" {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message["tool_call_id"], "output": message["content"]})
			continue
		}
		if toolCalls, ok := message["tool_calls"].([]any); ok {
			for _, rawCall := range toolCalls {
				call, _ := rawCall.(map[string]any)
				function, _ := call["function"].(map[string]any)
				input = append(input, map[string]any{"type": "function_call", "call_id": call["id"], "name": function["name"], "arguments": function["arguments"]})
			}
		}
		if _, exists := message["content"]; exists {
			input = append(input, map[string]any{"role": role, "content": message["content"]})
		}
	}
	response["input"] = input
	if tools, ok := chat["tools"].([]any); ok {
		converted := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, valid := rawTool.(map[string]any)
			if !valid {
				return nil, fmt.Errorf("tools entries must be objects")
			}
			if tool["type"] == "function" {
				fn, valid := tool["function"].(map[string]any)
				if !valid {
					return nil, fmt.Errorf("function tool requires function")
				}
				name, valid := fn["name"].(string)
				if !valid || strings.TrimSpace(name) == "" {
					return nil, fmt.Errorf("function tool requires function.name")
				}
				fn["type"] = "function"
				converted = append(converted, fn)
			} else {
				converted = append(converted, tool)
			}
		}
		response["tools"] = converted
	}
	return json.Marshal(response)
}

func (s *Server) proxyChatJSON(w http.ResponseWriter, r *http.Request, keyID, model string, resp *http.Response) {
	fallbackEventHash := s.randomUsageEventHash()
	value, input, output, status, err := completedResponse(resp)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	response, _ := value.(map[string]any)
	id, _ := response["id"].(string)
	created := s.now().Unix()
	if createdValue, ok := response["created_at"].(float64); ok {
		created = int64(createdValue)
	}
	message := map[string]any{"role": "assistant", "content": ""}
	var text strings.Builder
	var toolCalls []any
	if outputItems, ok := response["output"].([]any); ok {
		for _, rawItem := range outputItems {
			item, _ := rawItem.(map[string]any)
			switch item["type"] {
			case "message":
				if contents, ok := item["content"].([]any); ok {
					for _, rawContent := range contents {
						content, _ := rawContent.(map[string]any)
						if content["type"] == "output_text" {
							if part, ok := content["text"].(string); ok {
								text.WriteString(part)
							}
						}
					}
				}
			case "function_call":
				toolCalls = append(toolCalls, map[string]any{"id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": item["arguments"]}})
			}
		}
	}
	message["content"] = text.String()
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
		s.addUsage(keyID, s.usageEventHash(id, fallbackEventHash), model, input, output)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) proxyChatStream(w http.ResponseWriter, r *http.Request, keyID, model string, resp *http.Response) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	id := ""
	fallbackEventHash := s.randomUsageEventHash()
	created := s.now().Unix()
	var input, output int64
	sentRole := false
	terminal := false
	hasToolCalls := false
	for scanner.Scan() {
		data := sseData(scanner.Bytes())
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		var event map[string]any
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		eventType, _ := event["type"].(string)
		if eventID := responseIDFromEvent(data); eventID != "" {
			id = eventID
		}
		i, o := usageFromEvent(data)
		if i > input {
			input = i
		}
		if o > output {
			output = o
		}
		delta := map[string]any{}
		var finish any
		switch eventType {
		case "response.output_item.added":
			item, _ := event["item"].(map[string]any)
			if item["type"] != "function_call" {
				continue
			}
			delta["tool_calls"] = []any{map[string]any{"index": event["output_index"], "id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": ""}}}
			hasToolCalls = true
		case "response.output_text.delta":
			delta["content"] = event["delta"]
		case "response.function_call_arguments.delta":
			delta["tool_calls"] = []any{map[string]any{"index": event["output_index"], "function": map[string]any{"arguments": event["delta"]}}}
		case "response.completed":
			terminal = true
			finish = "stop"
			if hasToolCalls {
				finish = "tool_calls"
			}
		case "response.incomplete":
			terminal = true
			finish = "length"
		default:
			continue
		}
		if !sentRole {
			delta["role"] = "assistant"
			sentRole = true
		}
		chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		writeSSE(w, chunk)
	}
	if scanner.Err() != nil || !terminal {
		return
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if input > 0 || output > 0 {
		s.addUsage(keyID, s.usageEventHash(id, fallbackEventHash), model, input, output)
	}
}

func (s *Server) proxyCompatibleChatJSON(w http.ResponseWriter, keyID, model string, resp *http.Response) {
	var value any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&value); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	raw, _ := json.Marshal(value)
	input, output := usageFromEvent(raw)
	responseID := ""
	if payload, ok := value.(map[string]any); ok {
		responseID, _ = payload["id"].(string)
	}
	if input > 0 || output > 0 {
		s.addUsage(keyID, s.usageEventHash(responseID, s.randomUsageEventHash()), model, input, output)
	}
	copyResponseHeaders(w.Header(), resp.Header)
	writeJSON(w, resp.StatusCode, value)
}

func (s *Server) proxyCompatibleChatStream(w http.ResponseWriter, keyID, model string, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(resp.StatusCode)
	responseID := ""
	input, output, err := copySSE(w, resp.Body, func(data []byte) {
		if id := responseIDFromEvent(data); id != "" {
			responseID = id
		}
	})
	if err == nil && (input > 0 || output > 0) {
		s.addUsage(keyID, s.usageEventHash(responseID, s.randomUsageEventHash()), model, input, output)
	}
}

func writeSSE(w http.ResponseWriter, value any) {
	raw, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}
