package protocol

import (
	"encoding/json"
	"fmt"
	"github.com/gesta-run/subpool/internal/protocol/responseevent"
	"io"
	"net/http"
	"strings"
)

func (s *Renderer) proxyCopilotResponsesJSON(w http.ResponseWriter, model string, copilotTools map[string]ToolInfo, resp *http.Response) {
	var completion map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&completion); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	fallbackID := fmt.Sprintf("subpool-%d-%d", s.hooks.Now().UnixNano(), s.hooks.NextSequence())
	response, input, output, err := ChatCompletionToResponse(completion, model, fallbackID, copilotTools)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	responseID, _ := response["id"].(string)
	if input > 0 || output > 0 {
		s.hooks.Usage(responseID, model, input, output)
	}
	if responseID != "" {
		s.hooks.Session(responseID)
	}
	copyResponseHeaders(w.Header(), resp.Header)
	writeJSON(w, http.StatusOK, response)
}

func ChatCompletionToResponse(completion map[string]any, fallbackModel, fallbackID string, copilotTools map[string]ToolInfo) (map[string]any, int64, int64, error) {
	choices, ok := completion["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil, 0, 0, fmt.Errorf("chat completion has no choices")
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil, 0, 0, fmt.Errorf("chat completion choice is invalid")
	}
	message, ok := choice["message"].(map[string]any)
	if !ok {
		return nil, 0, 0, fmt.Errorf("chat completion message is missing")
	}
	chatID, _ := completion["id"].(string)
	responseID := copilotResponseID(chatID, fallbackID)
	model, _ := completion["model"].(string)
	if model == "" {
		model = fallbackModel
	}
	created := responseevent.Number(completion["created"])
	output, err := chatMessageToResponseOutput(message, responseID, copilotTools)
	if err != nil {
		return nil, 0, 0, err
	}
	finish, _ := choice["finish_reason"].(string)
	status, incomplete := responseStatus(finish)
	usage, input, outputTokens := chatUsageToResponses(completion["usage"])
	response := newCopilotResponse(responseID, model, created, status, output, usage)
	response["incomplete_details"] = incomplete
	return response, input, outputTokens, nil
}

func chatMessageToResponseOutput(message map[string]any, responseID string, copilotTools map[string]ToolInfo) ([]any, error) {
	output := make([]any, 0, 2)
	content := make([]any, 0, 1)
	if text, ok := message["content"].(string); ok && text != "" {
		content = append(content, map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}})
	}
	if refusal, ok := message["refusal"].(string); ok && refusal != "" {
		content = append(content, map[string]any{"type": "refusal", "refusal": refusal})
	}
	if len(content) > 0 {
		output = append(output, map[string]any{
			"id": copilotMessageID(responseID), "type": "message", "status": "completed", "role": "assistant", "content": content,
		})
	}
	if calls, ok := message["tool_calls"].([]any); ok {
		for _, rawCall := range calls {
			call, valid := rawCall.(map[string]any)
			callType, _ := call["type"].(string)
			if !valid || (callType != "" && callType != "function") {
				return nil, fmt.Errorf("chat completion tool call is invalid")
			}
			function, valid := call["function"].(map[string]any)
			if !valid {
				return nil, fmt.Errorf("chat completion function call is invalid")
			}
			callID, _ := call["id"].(string)
			name, _ := function["name"].(string)
			arguments, _ := function["arguments"].(string)
			if callID == "" || name == "" {
				return nil, fmt.Errorf("chat completion function call is incomplete")
			}
			info, mapped := copilotTools[name]
			if mapped && info.Custom {
				output = append(output, responseCustomToolCall(callID, info.Name, CustomToolInput(arguments), "completed", info.Namespace))
			} else {
				if !mapped {
					info.Name = name
				}
				output = append(output, responseFunctionCall(callID, info.Name, arguments, "completed", info.Namespace))
			}
		}
	}
	return output, nil
}

func responseFunctionCall(callID, name, arguments, status string, namespace ...string) map[string]any {
	item := map[string]any{
		"id": callID, "type": "function_call", "status": status, "call_id": callID, "name": name, "arguments": arguments,
	}
	if len(namespace) > 0 && namespace[0] != "" {
		item["namespace"] = namespace[0]
	}
	return item
}

func responseCustomToolCall(callID, name, input, status string, namespace ...string) map[string]any {
	item := map[string]any{
		"id": callID, "type": "custom_tool_call", "status": status, "call_id": callID, "name": name, "input": input,
	}
	if len(namespace) > 0 && namespace[0] != "" {
		item["namespace"] = namespace[0]
	}
	return item
}

func CustomToolInput(arguments string) string {
	var value map[string]any
	if json.Unmarshal([]byte(arguments), &value) == nil {
		if input, ok := value["input"].(string); ok {
			return input
		}
	}
	return arguments
}

func responseStatus(finish string) (string, any) {
	switch finish {
	case "length":
		return "incomplete", map[string]any{"reason": "max_output_tokens"}
	case "content_filter":
		return "incomplete", map[string]any{"reason": "content_filter"}
	default:
		return "completed", nil
	}
}

func chatUsageToResponses(value any) (map[string]any, int64, int64) {
	usage, _ := value.(map[string]any)
	input := responseevent.Number(usage["prompt_tokens"])
	output := responseevent.Number(usage["completion_tokens"])
	inputDetails, _ := usage["prompt_tokens_details"].(map[string]any)
	outputDetails, _ := usage["completion_tokens_details"].(map[string]any)
	return map[string]any{
		"input_tokens": input, "input_tokens_details": map[string]any{"cached_tokens": responseevent.Number(inputDetails["cached_tokens"])},
		"output_tokens": output, "output_tokens_details": map[string]any{"reasoning_tokens": responseevent.Number(outputDetails["reasoning_tokens"])},
		"total_tokens": input + output,
	}, input, output
}

func newCopilotResponse(id, model string, created int64, status string, output []any, usage any) map[string]any {
	return map[string]any{
		"id": id, "object": "response", "created_at": created, "status": status, "model": model, "output": output,
		"error": nil, "incomplete_details": nil, "parallel_tool_calls": true, "store": false, "usage": usage,
	}
}

func copilotResponseID(chatID, fallback string) string {
	if strings.HasPrefix(chatID, "resp_") {
		return chatID
	}
	id := strings.TrimPrefix(strings.TrimPrefix(chatID, "chatcmpl-"), "chat-")
	if id == "" {
		id = fallback
	}
	return "resp_" + id
}

func copilotMessageID(responseID string) string {
	return "msg_" + strings.TrimPrefix(responseID, "resp_")
}
