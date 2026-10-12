package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gesta-run/subpool/internal/jsonobject"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"strings"
)

func ChatToResponses(raw []byte) ([]byte, error) {
	var chat map[string]any
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("invalid JSON request")
	}
	if err := validateChatResponsesControls(chat); err != nil {
		return nil, err
	}
	response := make(map[string]any)
	for _, name := range []string{"model", "max_output_tokens", "reasoning", "parallel_tool_calls", "temperature", "top_p"} {
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
	if choice, exists := chat["tool_choice"]; exists {
		converted, convertErr := chatToolChoiceToResponses(choice)
		if convertErr != nil {
			return nil, convertErr
		}
		response["tool_choice"] = converted
	}
	if format, exists := chat["response_format"]; exists {
		converted, convertErr := chatResponseFormatToResponses(format)
		if convertErr != nil {
			return nil, convertErr
		}
		response["text"] = map[string]any{"format": converted}
	}
	response["stream"] = true
	response["store"] = false
	response["instructions"] = ""
	messages, ok := chat["messages"].([]any)
	if !ok {
		return nil, fmt.Errorf("messages is required")
	}
	input, err := chatMessagesToResponses(messages)
	if err != nil {
		return nil, err
	}
	response["input"] = input
	if tools, ok := chat["tools"].([]any); ok {
		converted := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, err := chatToolToResponses(rawTool)
			if err != nil {
				return nil, err
			}
			converted = append(converted, tool)
		}
		response["tools"] = converted
	}
	return json.Marshal(response)
}

func validateChatResponsesControls(chat map[string]any) error {
	for _, name := range []string{"n", "stop", "logit_bias", "frequency_penalty", "presence_penalty", "seed", "logprobs", "top_logprobs"} {
		if chatResponsesControlIsDefault(name, chat[name]) {
			continue
		}
		return &RequestError{Code: "unsupported_parameter", Message: fmt.Sprintf("Chat Completions parameter %q cannot be represented by Responses; use a provider with native Chat Completions support", name)}
	}
	return nil
}

func chatResponsesControlIsDefault(name string, value any) bool {
	if value == nil {
		return true
	}
	switch name {
	case "n":
		return value == float64(1)
	case "frequency_penalty", "presence_penalty", "top_logprobs":
		return value == float64(0)
	case "logprobs":
		return value == false
	case "stop":
		parts, ok := value.([]any)
		return ok && len(parts) == 0
	case "logit_bias":
		bias, ok := value.(map[string]any)
		return ok && len(bias) == 0
	default:
		return false
	}
}

func chatMessagesToResponses(messages []any) ([]any, error) {
	var input []any
	callTypes := make(map[string]string)
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("messages entries must be objects")
		}
		role, _ := message["role"].(string)
		content, hasContent := message["content"]
		if role == "tool" {
			if !hasContent || content == nil {
				return nil, fmt.Errorf("tool message requires content")
			}
			callID, _ := message["tool_call_id"].(string)
			if callID == "" {
				return nil, fmt.Errorf("tool message requires tool_call_id")
			}
			output, err := chatToolOutputToResponses(content)
			if err != nil {
				return nil, err
			}
			kind := "function_call_output"
			if callTypes[callID] == "custom_tool_call" {
				kind = "custom_tool_call_output"
			}
			input = append(input, map[string]any{"type": kind, "call_id": callID, "output": output})
			continue
		}
		toolCalls, hasToolCalls := message["tool_calls"].([]any)
		if !hasContent || content == nil {
			if role != "assistant" || !hasToolCalls || len(toolCalls) == 0 {
				return nil, fmt.Errorf("%s message requires content", role)
			}
		} else {
			converted, err := chatContentToResponses(role, content)
			if err != nil {
				return nil, err
			}
			input = append(input, map[string]any{"role": role, "content": converted})
		}
		for _, rawCall := range toolCalls {
			call, err := chatToolCallToResponses(rawCall)
			if err != nil {
				return nil, err
			}
			callTypes[call["call_id"].(string)] = call["type"].(string)
			input = append(input, call)
		}
	}
	return input, nil
}

func chatContentToResponses(role string, content any) (any, error) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	parts, ok := content.([]any)
	if !ok {
		return nil, fmt.Errorf("message content must be a string or an array")
	}
	converted := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, valid := rawPart.(map[string]any)
		if !valid {
			return nil, fmt.Errorf("message content entries must be objects")
		}
		partType, _ := part["type"].(string)
		switch partType {
		case "text", "input_text", "output_text":
			text, valid := part["text"].(string)
			if !valid {
				return nil, fmt.Errorf("%s content requires text", partType)
			}
			if role == "assistant" {
				converted = append(converted, map[string]any{"type": "output_text", "text": text})
			} else {
				converted = append(converted, map[string]any{"type": "input_text", "text": text})
			}
		case "image_url":
			image, err := chatImageToResponses(part["image_url"])
			if err != nil {
				return nil, err
			}
			converted = append(converted, image)
		default:
			return nil, fmt.Errorf("content type %q is not supported for Responses", partType)
		}
	}
	return converted, nil
}

func chatImageToResponses(value any) (map[string]any, error) {
	if url, ok := value.(string); ok && strings.TrimSpace(url) != "" {
		return map[string]any{"type": "input_image", "image_url": url}, nil
	}
	image, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("image_url content requires image_url.url")
	}
	url, _ := image["url"].(string)
	if strings.TrimSpace(url) == "" {
		return nil, fmt.Errorf("image_url content requires image_url.url")
	}
	converted := map[string]any{"type": "input_image", "image_url": url}
	if detail, ok := image["detail"].(string); ok && detail != "" {
		converted["detail"] = detail
	}
	return converted, nil
}

func chatToolChoiceToResponses(value any) (any, error) {
	if _, ok := value.(string); ok {
		return value, nil
	}
	choice, ok := value.(map[string]any)
	if !ok || (choice["type"] != "function" && choice["type"] != "custom") {
		return nil, fmt.Errorf("tool_choice must be a string or a function/custom choice")
	}
	kind := choice["type"].(string)
	function, ok := choice[kind].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s tool_choice requires %s", kind, kind)
	}
	name, ok := function["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%s tool_choice requires %s.name", kind, kind)
	}
	return map[string]any{"type": kind, "name": name}, nil
}

func chatToolToResponses(value any) (map[string]any, error) {
	tool, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tools entries must be objects")
	}
	kind, _ := tool["type"].(string)
	if kind != "function" && kind != "custom" {
		return nil, fmt.Errorf("Chat tool type %q is not supported for Responses", kind)
	}
	definition, ok := tool[kind].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s tool requires %s", kind, kind)
	}
	name, _ := definition["name"].(string)
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("%s tool requires %s.name", kind, kind)
	}
	definition["type"] = kind
	if kind == "custom" {
		if format, ok := definition["format"].(map[string]any); ok && format["type"] == "grammar" {
			grammar, ok := format["grammar"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("custom tool grammar requires grammar")
			}
			definition["format"] = map[string]any{"type": "grammar", "syntax": grammar["syntax"], "definition": grammar["definition"]}
		}
	}
	return definition, nil
}

func chatToolCallToResponses(value any) (map[string]any, error) {
	call, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool_calls entries must be objects")
	}
	callID, _ := call["id"].(string)
	kind, _ := call["type"].(string)
	if kind == "" {
		kind = "function"
	}
	if kind != "function" && kind != "custom" {
		return nil, fmt.Errorf("tool call type %q is not supported for Responses", kind)
	}
	definition, _ := call[kind].(map[string]any)
	name, _ := definition["name"].(string)
	if callID == "" || name == "" {
		return nil, fmt.Errorf("%s tool call requires id and name", kind)
	}
	field, itemType := "arguments", "function_call"
	if kind == "custom" {
		field, itemType = "input", "custom_tool_call"
	}
	content, ok := definition[field].(string)
	if !ok {
		return nil, fmt.Errorf("%s tool call requires %s", kind, field)
	}
	return map[string]any{"type": itemType, "call_id": callID, "name": name, field: content}, nil
}

func chatToolOutputToResponses(content any) (any, error) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	parts, ok := content.([]any)
	if !ok {
		return nil, fmt.Errorf("tool message content must be a string or text parts")
	}
	converted := make([]any, 0, len(parts))
	for _, value := range parts {
		part, ok := value.(map[string]any)
		text, valid := part["text"].(string)
		if !ok || !valid || part["type"] != "text" {
			return nil, fmt.Errorf("tool message content supports only text parts")
		}
		converted = append(converted, map[string]any{"type": "input_text", "text": text})
	}
	return converted, nil
}

func chatResponseFormatToResponses(value any) (map[string]any, error) {
	format, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response_format must be an object")
	}
	formatType, _ := format["type"].(string)
	switch formatType {
	case "text", "json_object":
		return map[string]any{"type": formatType}, nil
	case "json_schema":
		schema, ok := format["json_schema"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("json_schema response_format requires json_schema")
		}
		converted := map[string]any{"type": "json_schema"}
		for _, field := range []string{"name", "description", "schema", "strict"} {
			if fieldValue, exists := schema[field]; exists {
				converted[field] = fieldValue
			}
		}
		return converted, nil
	default:
		return nil, fmt.Errorf("response_format type %q is not supported for Responses", formatType)
	}
}

func NormalizeCodexRequest(body []byte, installationID string, fastMode bool) ([]byte, error) {
	return normalizeCodexPayload(body, installationID, fastMode, false)
}

func NormalizeCodexWebSocketRequest(body []byte, installationID string, fastMode bool) ([]byte, error) {
	return normalizeCodexPayload(body, installationID, fastMode, true)
}

func normalizeCodexPayload(body []byte, installationID string, fastMode, webSocket bool) ([]byte, error) {
	value, err := jsonobject.Parse(body)
	if err != nil {
		return nil, fmt.Errorf("decode Codex request: %w", err)
	}
	if webSocket {
		value.Delete("stream")
	} else {
		_ = value.Set("stream", []byte("true"))
	}
	_ = value.Set("store", []byte("false"))
	if fastMode {
		_ = value.Set("service_tier", []byte(`"priority"`))
	} else {
		value.Delete("service_tier")
	}
	for _, unsupported := range []string{"temperature", "top_p", "logprobs", "top_logprobs"} {
		value.Delete(unsupported)
	}
	if instructions, ok := value.Value("instructions"); !ok || bytes.Equal(bytes.TrimSpace(instructions), []byte("null")) {
		_ = value.Set("instructions", []byte(`""`))
	}
	if input, ok := value.Value("input"); ok && len(input) > 0 && input[0] == '"' {
		wrapped := make([]byte, 0, len(input)+80)
		wrapped = append(wrapped, `[{"role":"user","content":[{"type":"input_text","text":`...)
		wrapped = append(wrapped, input...)
		wrapped = append(wrapped, `}]}]`...)
		_ = value.Set("input", wrapped)
	}
	metadata, exists := value.Value("client_metadata")
	rewrittenMetadata, err := rewriteClientMetadata(metadata, exists, installationID)
	if err != nil {
		return nil, err
	}
	_ = value.Set("client_metadata", rewrittenMetadata)
	return value.Bytes(), nil
}

func ForceStream(body []byte) []byte {
	value, err := jsonobject.Parse(body)
	if err != nil {
		return body
	}
	_ = value.Set("stream", []byte("true"))
	return value.Bytes()
}

func rewriteClientMetadata(raw []byte, exists bool, installationID string) ([]byte, error) {
	metadata := make(map[string]any)
	if exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
			return nil, codex.ErrInvalidClientMetadata
		}
	}
	request := map[string]any{"client_metadata": metadata}
	if err := codex.ApplyDeviceIdentity(request, installationID); err != nil {
		return nil, err
	}
	return json.Marshal(request["client_metadata"])
}
