package protocol

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

func ResponsesToChat(raw []byte, allowToolFallback bool) ([]byte, map[string]ToolInfo, error) {
	return responsesToChat(raw, allowToolFallback, "max_completion_tokens")
}

// Copilot retains its existing token-limit field independently of the shared codec.
func ResponsesToCopilotChat(raw []byte, allowToolFallback bool) ([]byte, map[string]ToolInfo, error) {
	return responsesToChat(raw, allowToolFallback, "max_tokens")
}

func responsesToChat(raw []byte, allowToolFallback bool, tokenLimitField string) ([]byte, map[string]ToolInfo, error) {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON request")
	}
	if previous, _ := response["previous_response_id"].(string); strings.TrimSpace(previous) != "" {
		return nil, nil, fmt.Errorf("previous_response_id is not supported by Chat Completions compatibility; replay the complete conversation input")
	}
	if response["context_management"] != nil {
		return nil, nil, &RequestError{Code: "unsupported_compaction", Message: "Chat Completions cannot represent Responses context_management compaction"}
	}
	chat := make(map[string]any)
	copilotTools := make(map[string]ToolInfo)
	for _, name := range []string{"model", "stream", "temperature", "top_p", "parallel_tool_calls"} {
		if value, exists := response[name]; exists {
			chat[name] = value
		}
	}
	if maximum, exists := response["max_output_tokens"]; exists {
		chat[tokenLimitField] = maximum
	}
	if reasoning, ok := response["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok && effort != "" {
			chat["reasoning_effort"] = effort
		}
	}
	toolValue, hasTools := response["tools"]
	tools, err := responseToolsWithAdditional(toolValue, hasTools, response["input"])
	if err != nil {
		return nil, nil, err
	}
	if len(tools) > 0 {
		converted, mappings, convertErr := responsesToolsToChat(tools, allowToolFallback)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		if len(converted) > 0 {
			chat["tools"] = converted
		}
		copilotTools = mappings
	}
	messages, err := responsesInputToChat(response["input"], copilotTools)
	if err != nil {
		return nil, nil, err
	}
	if instructions, exists := response["instructions"]; exists && instructions != nil {
		text, ok := instructions.(string)
		if !ok {
			return nil, nil, fmt.Errorf("instructions must be a string")
		}
		if text != "" {
			messages = append([]any{map[string]any{"role": "system", "content": text}}, messages...)
		}
	}
	chat["messages"] = messages
	if choice, exists := response["tool_choice"]; exists {
		converted, convertErr := responseToolChoiceToChat(choice, copilotTools, allowToolFallback)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		if _, hasTools := chat["tools"]; converted != nil && hasTools {
			chat["tool_choice"] = converted
		}
	}
	if text, exists := response["text"]; exists {
		format, convertErr := responseTextFormatToChat(text)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		if format != nil {
			chat["response_format"] = format
		}
	}
	if stream, _ := chat["stream"].(bool); stream {
		chat["stream_options"] = map[string]any{"include_usage": true}
	}
	body, err := json.Marshal(chat)
	return body, copilotTools, err
}

func responsesInputToChat(input any, copilotTools map[string]ToolInfo) ([]any, error) {
	if text, ok := input.(string); ok {
		return []any{map[string]any{"role": "user", "content": text}}, nil
	}
	items, ok := input.([]any)
	if !ok {
		return nil, fmt.Errorf("input must be a string or an array")
	}
	messages := make([]any, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("input entries must be objects")
		}
		switch itemType, _ := item["type"].(string); itemType {
		case "", "message":
			message, err := responseMessageToChat(item)
			if err != nil {
				return nil, err
			}
			messages = append(messages, message)
		case "function_call":
			if err := appendResponseFunctionCall(&messages, item, copilotTools); err != nil {
				return nil, err
			}
		case "custom_tool_call":
			input, ok := item["input"].(string)
			if !ok {
				return nil, fmt.Errorf("custom_tool_call requires input")
			}
			arguments, _ := json.Marshal(map[string]any{"input": input})
			customCall := map[string]any{
				"call_id": item["call_id"], "name": item["name"], "namespace": item["namespace"], "arguments": string(arguments),
			}
			if err := appendResponseFunctionCall(&messages, customCall, copilotTools); err != nil {
				return nil, err
			}
		case "function_call_output", "custom_tool_call_output":
			callID, _ := item["call_id"].(string)
			if callID == "" {
				return nil, fmt.Errorf("function_call_output requires call_id")
			}
			content, err := stringifyToolOutput(item["output"])
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": content})
		case "additional_tools":
			continue
		case "reasoning":
			continue
		default:
			return nil, fmt.Errorf("input type %q is not supported for GitHub Copilot", itemType)
		}
	}
	return messages, nil
}

func responseMessageToChat(item map[string]any) (map[string]any, error) {
	role, _ := item["role"].(string)
	if role == "developer" {
		role = "system"
	}
	if role != "user" && role != "assistant" && role != "system" {
		return nil, fmt.Errorf("input message has unsupported role %q", role)
	}
	content, err := responseContentToChat(item["content"])
	if err != nil {
		return nil, err
	}
	return map[string]any{"role": role, "content": content}, nil
}

func responseContentToChat(content any) (any, error) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	parts, ok := content.([]any)
	if !ok {
		return nil, fmt.Errorf("message content must be a string or an array")
	}
	converted := make([]any, 0, len(parts))
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("message content entries must be objects")
		}
		switch partType, _ := part["type"].(string); partType {
		case "input_text", "output_text", "text":
			text, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("%s content requires text", partType)
			}
			converted = append(converted, map[string]any{"type": "text", "text": text})
		case "input_image":
			url, ok := part["image_url"].(string)
			if !ok || url == "" {
				return nil, fmt.Errorf("input_image content requires image_url")
			}
			image := map[string]any{"url": url}
			if detail, ok := part["detail"].(string); ok {
				image["detail"] = detail
			}
			converted = append(converted, map[string]any{"type": "image_url", "image_url": image})
		default:
			return nil, fmt.Errorf("content type %q is not supported for GitHub Copilot", partType)
		}
	}
	return converted, nil
}

func appendResponseFunctionCall(messages *[]any, item map[string]any, copilotTools map[string]ToolInfo) error {
	callID, _ := item["call_id"].(string)
	name, _ := item["name"].(string)
	namespace, _ := item["namespace"].(string)
	arguments, _ := item["arguments"].(string)
	if callID == "" || name == "" {
		return fmt.Errorf("function_call requires call_id and name")
	}
	name = CopilotToolName(name, namespace, copilotTools)
	call := map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}
	if len(*messages) > 0 {
		if previous, ok := (*messages)[len(*messages)-1].(map[string]any); ok && previous["role"] == "assistant" && previous["content"] == nil {
			if calls, valid := previous["tool_calls"].([]any); valid {
				previous["tool_calls"] = append(calls, call)
				return nil
			}
		}
	}
	*messages = append(*messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{call}})
	return nil
}

func stringifyToolOutput(output any) (string, error) {
	if text, ok := output.(string); ok {
		return text, nil
	}
	if parts, ok := output.([]any); ok {
		var text strings.Builder
		for _, value := range parts {
			part, valid := value.(map[string]any)
			if !valid || part["type"] != "input_text" {
				return "", &RequestError{Code: "unsupported_tool_output", Message: "Chat Completions cannot represent image/file tool outputs; select a provider with native Responses support"}
			}
			value, valid := part["text"].(string)
			if !valid {
				return "", fmt.Errorf("input_text tool output requires text")
			}
			text.WriteString(value)
		}
		return text.String(), nil
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("invalid tool output: %w", err)
	}
	return string(raw), nil
}

func responsesToolsToChat(value any, allowToolFallback bool) ([]any, map[string]ToolInfo, error) {
	tools, ok := value.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("tools must be an array")
	}
	converted := make([]any, 0, len(tools))
	mappings := make(map[string]ToolInfo)
	usedNames := make(map[string]struct{})
	hasDeclaredSearchInventory := false
	for _, rawTool := range tools {
		tool, valid := rawTool.(map[string]any)
		if !valid {
			continue
		}
		children, _ := tool["tools"].([]any)
		deferred, _ := tool["defer_loading"].(bool)
		if (tool["type"] == "namespace" && len(children) > 0) || ((tool["type"] == "function" || tool["type"] == "mcp") && deferred) {
			hasDeclaredSearchInventory = true
		}
		if tool["type"] == "function" || tool["type"] == "custom" {
			if name, _ := tool["name"].(string); name != "" {
				usedNames[name] = struct{}{}
			}
		}
	}
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("tools entries must be objects")
		}
		toolType, _ := tool["type"].(string)
		switch toolType {
		case "function":
			function, err := responseFunctionToolToChat(tool, "")
			if err != nil {
				return nil, nil, err
			}
			converted = append(converted, map[string]any{"type": "function", "function": function})
		case "custom":
			name, _ := tool["name"].(string)
			function, err := responseCustomToolToChat(tool, "")
			if err != nil {
				return nil, nil, err
			}
			mappings[name] = ToolInfo{Name: name, Custom: true}
			converted = append(converted, map[string]any{"type": "function", "function": function})
		case "namespace":
			namespace, _ := tool["name"].(string)
			if namespace == "" {
				return nil, nil, fmt.Errorf("namespace tool requires name")
			}
			children, ok := tool["tools"].([]any)
			if !ok {
				return nil, nil, fmt.Errorf("namespace tool requires tools")
			}
			for _, rawChild := range children {
				child, valid := rawChild.(map[string]any)
				if !valid {
					return nil, nil, fmt.Errorf("namespace tools entries must be objects")
				}
				childType, _ := child["type"].(string)
				if childType != "function" && childType != "custom" {
					return nil, nil, fmt.Errorf("namespace tool type %q is not supported for GitHub Copilot", childType)
				}
				name, _ := child["name"].(string)
				upstreamName := namespacedCopilotToolName(namespace, name, usedNames)
				function, err := responseFunctionToolToChat(child, upstreamName)
				if childType == "custom" {
					function, err = responseCustomToolToChat(child, upstreamName)
				}
				if err != nil {
					return nil, nil, err
				}
				usedNames[upstreamName] = struct{}{}
				mappings[upstreamName] = ToolInfo{Name: name, Namespace: namespace, Custom: childType == "custom"}
				converted = append(converted, map[string]any{"type": "function", "function": function})
			}
		case "tool_search":
			execution, exists := tool["execution"]
			// Resolve declared inventories eagerly; fallback handling omits inventories Copilot cannot represent.
			if !exists || execution == "server" || (execution == "client" && hasDeclaredSearchInventory) {
				continue
			}
			if execution == "client" {
				return nil, nil, fmt.Errorf("client tool_search requires a declared searchable tool inventory for GitHub Copilot")
			}
			if mode, ok := execution.(string); ok {
				return nil, nil, fmt.Errorf("tool_search execution %q is not supported for GitHub Copilot", mode)
			}
			return nil, nil, fmt.Errorf("tool_search execution must be \"server\" for GitHub Copilot")
		default:
			if allowToolFallback && isCopilotOmittableTool(toolType) {
				continue
			}
			return nil, nil, fmt.Errorf("tool type %q is not supported for GitHub Copilot", toolType)
		}
	}
	return converted, mappings, nil
}

func responseFunctionToolToChat(tool map[string]any, name string) (map[string]any, error) {
	originalName, _ := tool["name"].(string)
	if originalName == "" {
		return nil, fmt.Errorf("function tool requires name")
	}
	if name == "" {
		name = originalName
	}
	function := map[string]any{"name": name}
	for _, field := range []string{"description", "parameters", "strict"} {
		if fieldValue, exists := tool[field]; exists {
			function[field] = fieldValue
		}
	}
	return function, nil
}

func namespacedCopilotToolName(namespace, name string, used map[string]struct{}) string {
	base := namespace + "__" + name
	if len(base) <= MaxCopilotToolNameLength {
		if _, exists := used[base]; !exists {
			return base
		}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(namespace+"\x00"+name)))
	for hashLength := 12; hashLength <= len(digest); hashLength += 4 {
		suffix := "__" + digest[:hashLength]
		prefixLength := MaxCopilotToolNameLength - len(suffix)
		candidate := base
		if len(candidate) > prefixLength {
			candidate = candidate[:prefixLength]
		}
		candidate += suffix
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	return digest[:MaxCopilotToolNameLength]
}

func CopilotToolName(name, namespace string, mappings map[string]ToolInfo) string {
	if namespace == "" {
		return name
	}
	for upstreamName, info := range mappings {
		if info.Name == name && info.Namespace == namespace {
			return upstreamName
		}
	}
	return namespacedCopilotToolName(namespace, name, nil)
}

func responseTextFormatToChat(value any) (any, error) {
	text, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("text must be an object")
	}
	format, ok := text["format"].(map[string]any)
	if !ok || format["type"] == nil || format["type"] == "text" {
		return nil, nil
	}
	switch format["type"] {
	case "json_object":
		return map[string]any{"type": "json_object"}, nil
	case "json_schema":
		schema := make(map[string]any)
		for _, field := range []string{"name", "description", "schema", "strict"} {
			if fieldValue, exists := format[field]; exists {
				schema[field] = fieldValue
			}
		}
		return map[string]any{"type": "json_schema", "json_schema": schema}, nil
	default:
		return nil, fmt.Errorf("text format %q is not supported for GitHub Copilot", format["type"])
	}
}
