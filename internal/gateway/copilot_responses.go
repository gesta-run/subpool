package gateway

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gesta-run/subpool/internal/gateway/responseevent"
)

const maxCopilotToolNameLength = 64

type copilotToolInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Custom    bool   `json:"custom,omitempty"`
}

func responsesToChat(raw []byte) ([]byte, map[string]copilotToolInfo, error) {
	var response map[string]any
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON request")
	}
	if previous, _ := response["previous_response_id"].(string); strings.TrimSpace(previous) != "" {
		return nil, nil, fmt.Errorf("previous_response_id is not supported for GitHub Copilot Responses compatibility")
	}
	chat := make(map[string]any)
	copilotTools := make(map[string]copilotToolInfo)
	for _, name := range []string{"model", "stream", "temperature", "top_p", "parallel_tool_calls"} {
		if value, exists := response[name]; exists {
			chat[name] = value
		}
	}
	if maximum, exists := response["max_output_tokens"]; exists {
		chat["max_tokens"] = maximum
	}
	if reasoning, ok := response["reasoning"].(map[string]any); ok {
		if effort, ok := reasoning["effort"].(string); ok && effort != "" {
			chat["reasoning_effort"] = effort
		}
	}
	if tools, exists := response["tools"]; exists {
		converted, mappings, convertErr := responsesToolsToChat(tools)
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
		converted, convertErr := responseToolChoiceToChat(choice, copilotTools)
		if convertErr != nil {
			return nil, nil, convertErr
		}
		chat["tool_choice"] = converted
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

func responsesInputToChat(input any, copilotTools map[string]copilotToolInfo) ([]any, error) {
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
				"call_id": item["call_id"], "name": item["name"], "arguments": string(arguments),
			}
			if err := appendResponseFunctionCall(&messages, customCall, copilotTools); err != nil {
				return nil, err
			}
		case "function_call_output", "custom_tool_call_output":
			callID, _ := item["call_id"].(string)
			if callID == "" {
				return nil, fmt.Errorf("function_call_output requires call_id")
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": stringifyToolOutput(item["output"])})
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

func appendResponseFunctionCall(messages *[]any, item map[string]any, copilotTools map[string]copilotToolInfo) error {
	callID, _ := item["call_id"].(string)
	name, _ := item["name"].(string)
	namespace, _ := item["namespace"].(string)
	arguments, _ := item["arguments"].(string)
	if callID == "" || name == "" {
		return fmt.Errorf("function_call requires call_id and name")
	}
	name = copilotToolName(name, namespace, copilotTools)
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

func stringifyToolOutput(output any) string {
	if text, ok := output.(string); ok {
		return text
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return "null"
	}
	return string(raw)
}

func responsesToolsToChat(value any) ([]any, map[string]copilotToolInfo, error) {
	tools, ok := value.([]any)
	if !ok {
		return nil, nil, fmt.Errorf("tools must be an array")
	}
	converted := make([]any, 0, len(tools))
	mappings := make(map[string]copilotToolInfo)
	usedNames := make(map[string]struct{})
	hasDeclaredSearchInventory := false
	for _, rawTool := range tools {
		tool, valid := rawTool.(map[string]any)
		if !valid {
			continue
		}
		children, _ := tool["tools"].([]any)
		if (tool["type"] == "namespace" && len(children) > 0) || (tool["type"] == "function" && tool["defer_loading"] == true) {
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
			if name == "" {
				return nil, nil, fmt.Errorf("custom tool requires name")
			}
			mappings[name] = copilotToolInfo{Name: name, Custom: true}
			function := map[string]any{"name": name}
			if description, ok := tool["description"].(string); ok && description != "" {
				function["description"] = description
			}
			function["parameters"] = map[string]any{
				"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}},
				"required": []string{"input"}, "additionalProperties": false,
			}
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
				if child["type"] != "function" {
					return nil, nil, fmt.Errorf("namespace tool type %q is not supported for GitHub Copilot", child["type"])
				}
				name, _ := child["name"].(string)
				upstreamName := namespacedCopilotToolName(namespace, name, usedNames)
				function, err := responseFunctionToolToChat(child, upstreamName)
				if err != nil {
					return nil, nil, err
				}
				usedNames[upstreamName] = struct{}{}
				mappings[upstreamName] = copilotToolInfo{Name: name, Namespace: namespace}
				converted = append(converted, map[string]any{"type": "function", "function": function})
			}
		case "tool_search":
			execution, exists := tool["execution"]
			// Copilot cannot search tools, so eagerly load any inventory already declared by the client.
			if !exists || execution == "server" || (execution == "client" && hasDeclaredSearchInventory) {
				continue
			}
			if execution == "client" {
				return nil, nil, fmt.Errorf("client tool_search requires declared namespace or deferred function tools for GitHub Copilot")
			}
			if mode, ok := execution.(string); ok {
				return nil, nil, fmt.Errorf("tool_search execution %q is not supported for GitHub Copilot", mode)
			}
			return nil, nil, fmt.Errorf("tool_search execution must be \"server\" for GitHub Copilot")
		default:
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
	if len(base) <= maxCopilotToolNameLength {
		if _, exists := used[base]; !exists {
			return base
		}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(namespace+"\x00"+name)))
	for hashLength := 12; hashLength <= len(digest); hashLength += 4 {
		suffix := "__" + digest[:hashLength]
		prefixLength := maxCopilotToolNameLength - len(suffix)
		candidate := base
		if len(candidate) > prefixLength {
			candidate = candidate[:prefixLength]
		}
		candidate += suffix
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	return digest[:maxCopilotToolNameLength]
}

func copilotToolName(name, namespace string, mappings map[string]copilotToolInfo) string {
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

func setCopilotToolHeaders(header http.Header, mappings map[string]copilotToolInfo) {
	header.Del(customToolHeader)
	header.Del(namespaceToolHeader)
	for upstreamName, info := range mappings {
		if info.Custom {
			header.Add(customToolHeader, upstreamName)
		}
		if info.Namespace == "" {
			continue
		}
		payload, err := json.Marshal(struct {
			UpstreamName string `json:"upstream_name"`
			Name         string `json:"name"`
			Namespace    string `json:"namespace"`
		}{upstreamName, info.Name, info.Namespace})
		if err == nil {
			header.Add(namespaceToolHeader, base64.RawURLEncoding.EncodeToString(payload))
		}
	}
}

func copilotToolsFromHeaders(header http.Header) map[string]copilotToolInfo {
	mappings := make(map[string]copilotToolInfo)
	for _, name := range header.Values(customToolHeader) {
		mappings[name] = copilotToolInfo{Name: name, Custom: true}
	}
	for _, encoded := range header.Values(namespaceToolHeader) {
		payload, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		var value struct {
			UpstreamName string `json:"upstream_name"`
			Name         string `json:"name"`
			Namespace    string `json:"namespace"`
		}
		if json.Unmarshal(payload, &value) == nil && value.UpstreamName != "" && value.Name != "" && value.Namespace != "" {
			mappings[value.UpstreamName] = copilotToolInfo{Name: value.Name, Namespace: value.Namespace}
		}
	}
	return mappings
}

func responseToolChoiceToChat(value any, copilotTools map[string]copilotToolInfo) (any, error) {
	if choice, ok := value.(string); ok {
		switch choice {
		case "auto", "none", "required":
			return choice, nil
		default:
			return nil, fmt.Errorf("tool_choice %q is not supported for GitHub Copilot", choice)
		}
	}
	choice, ok := value.(map[string]any)
	if !ok || (choice["type"] != "function" && choice["type"] != "custom") {
		return nil, fmt.Errorf("tool_choice is not supported for GitHub Copilot")
	}
	name, _ := choice["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("function tool_choice requires name")
	}
	namespace, _ := choice["namespace"].(string)
	name = copilotToolName(name, namespace, copilotTools)
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
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

func (s *Server) proxyCopilotResponsesJSON(w http.ResponseWriter, keyID, poolID, accountID, model string, copilotTools map[string]copilotToolInfo, resp *http.Response) {
	var completion map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&completion); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	fallbackID := fmt.Sprintf("subpool-%d-%d", s.now().UnixNano(), s.eventSeq.Add(1))
	response, input, output, err := chatCompletionToResponse(completion, model, fallbackID, copilotTools)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	responseID, _ := response["id"].(string)
	if input > 0 || output > 0 {
		s.addUsage(keyID, s.usageEventHash(responseID, s.randomUsageEventHash()), model, input, output)
	}
	if responseID != "" && accountID != "" {
		s.saveSession(keyID, poolID, responseID, accountID)
	}
	copyResponseHeaders(w.Header(), resp.Header)
	writeJSON(w, http.StatusOK, response)
}

func chatCompletionToResponse(completion map[string]any, fallbackModel, fallbackID string, copilotTools map[string]copilotToolInfo) (map[string]any, int64, int64, error) {
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

func chatMessageToResponseOutput(message map[string]any, responseID string, copilotTools map[string]copilotToolInfo) ([]any, error) {
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
				output = append(output, responseCustomToolCall(callID, info.Name, customToolInput(arguments), "completed"))
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

func responseCustomToolCall(callID, name, input, status string) map[string]any {
	return map[string]any{
		"id": callID, "type": "custom_tool_call", "status": status, "call_id": callID, "name": name, "input": input,
	}
}

func customToolInput(arguments string) string {
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

type copilotResponsesStreamState struct {
	responseID   string
	model        string
	created      int64
	sequence     int64
	started      bool
	failed       bool
	finishReason string
	nextOutput   int
	message      *copilotStreamMessage
	tools        map[int]*copilotStreamTool
	inputTokens  int64
	outputTokens int64
	usage        map[string]any
	copilotTools map[string]copilotToolInfo
	limit        responseevent.LimitKind
}

type copilotStreamMessage struct {
	id           string
	outputIndex  int
	text         strings.Builder
	refusal      strings.Builder
	textIndex    int
	refusalIndex int
	nextContent  int
}

type copilotStreamTool struct {
	id           string
	name         strings.Builder
	responseName string
	namespace    string
	arguments    strings.Builder
	outputIndex  int
	added        bool
	custom       bool
}

func (s *Server) proxyCopilotResponsesStream(w http.ResponseWriter, keyID, poolID, accountID, model string, copilotTools map[string]copilotToolInfo, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	state, err := s.writeCopilotResponsesStream(w, model, copilotTools, resp.Body)
	s.recordProviderStreamLimit(accountID, state.limit)
	if err != nil || state.failed {
		return
	}
	if state.inputTokens > 0 || state.outputTokens > 0 {
		s.addUsage(keyID, s.usageEventHash(state.responseID, s.randomUsageEventHash()), state.model, state.inputTokens, state.outputTokens)
	}
	if state.responseID != "" && accountID != "" {
		s.saveSession(keyID, poolID, state.responseID, accountID)
	}
}

func (s *Server) writeCopilotResponsesStream(w http.ResponseWriter, model string, copilotTools map[string]copilotToolInfo, reader io.Reader) (*copilotResponsesStreamState, error) {
	fallbackID := fmt.Sprintf("subpool-%d-%d", s.now().UnixNano(), s.eventSeq.Add(1))
	state := &copilotResponsesStreamState{
		responseID: copilotResponseID("", fallbackID), model: model, created: s.now().Unix(),
		tools: make(map[int]*copilotStreamTool), copilotTools: copilotTools,
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	for scanner.Scan() {
		data := responseevent.SSEData(scanner.Bytes())
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal(data, &chunk) == nil {
			if state.limit == responseevent.LimitNone {
				state.limit = responseevent.ClassifyLimit(data)
			}
			state.consume(w, chunk)
			if state.failed {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return state, err
	}
	if state.failed {
		return state, nil
	}
	if !state.finish(w) {
		return state, fmt.Errorf("Copilot stream did not contain a terminal response")
	}
	return state, nil
}

type pipeResponseWriter struct {
	header http.Header
	writer io.Writer
}

func (w *pipeResponseWriter) Header() http.Header               { return w.header }
func (w *pipeResponseWriter) Write(payload []byte) (int, error) { return w.writer.Write(payload) }
func (w *pipeResponseWriter) WriteHeader(int)                   {}
func (w *pipeResponseWriter) Flush()                            {}

func (s *Server) bridgeCopilotResponses(resp *http.Response, model string) *http.Response {
	copilotTools := copilotToolsFromHeaders(resp.Header)
	header := resp.Header.Clone()
	header.Del(accountHeader)
	header.Del(formatHeader)
	header.Del(customToolHeader)
	header.Del(namespaceToolHeader)
	header.Set("Content-Type", "text/event-stream")
	reader, writer := io.Pipe()
	go func() {
		defer resp.Body.Close()
		sink := &pipeResponseWriter{header: make(http.Header), writer: writer}
		_, err := s.writeCopilotResponsesStream(sink, model, copilotTools, resp.Body)
		_ = writer.CloseWithError(err)
	}()
	return &http.Response{StatusCode: resp.StatusCode, Header: header, Body: reader}
}

func (s *copilotResponsesStreamState) consume(w http.ResponseWriter, chunk map[string]any) {
	if upstreamError, exists := chunk["error"]; exists && upstreamError != nil {
		s.writeError(w, upstreamError)
		return
	}
	if !s.started {
		if chatID, _ := chunk["id"].(string); chatID != "" {
			s.responseID = copilotResponseID(chatID, strings.TrimPrefix(s.responseID, "resp_"))
		}
		if model, _ := chunk["model"].(string); model != "" {
			s.model = model
		}
		if created := responseevent.Number(chunk["created"]); created > 0 {
			s.created = created
		}
	}
	if usage, input, output := chatUsageToResponses(chunk["usage"]); input > 0 || output > 0 {
		s.usage, s.inputTokens, s.outputTokens = usage, input, output
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	s.start(w)
	if content, ok := delta["content"].(string); ok && content != "" {
		s.writeTextDelta(w, content)
	}
	if refusal, ok := delta["refusal"].(string); ok && refusal != "" {
		s.writeRefusalDelta(w, refusal)
	}
	if calls, ok := delta["tool_calls"].([]any); ok {
		s.writeToolDeltas(w, calls)
	}
	if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
		s.finishReason = finish
	}
}

func (s *copilotResponsesStreamState) writeError(w http.ResponseWriter, value any) {
	code := "provider_error"
	message := "GitHub Copilot stream failed"
	var param any
	if upstream, ok := value.(map[string]any); ok {
		if upstreamCode, _ := upstream["code"].(string); upstreamCode != "" {
			code = upstreamCode
		} else if upstreamType, _ := upstream["type"].(string); upstreamType != "" {
			code = upstreamType
		}
		if upstreamMessage, _ := upstream["message"].(string); upstreamMessage != "" {
			message = upstreamMessage
		}
		param = upstream["param"]
	} else if upstreamMessage, ok := value.(string); ok && upstreamMessage != "" {
		message = upstreamMessage
	}
	s.failed = true
	s.emit(w, map[string]any{"type": "error", "code": code, "message": message, "param": param})
}

func (s *copilotResponsesStreamState) start(w http.ResponseWriter) {
	if s.started {
		return
	}
	s.started = true
	response := newCopilotResponse(s.responseID, s.model, s.created, "in_progress", []any{}, nil)
	s.emit(w, map[string]any{"type": "response.created", "response": response})
	s.emit(w, map[string]any{"type": "response.in_progress", "response": response})
}

func (s *copilotResponsesStreamState) writeTextDelta(w http.ResponseWriter, delta string) {
	message := s.ensureMessage(w)
	if message.textIndex < 0 {
		message.textIndex = message.nextContent
		message.nextContent++
		part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}
		s.emit(w, map[string]any{"type": "response.content_part.added", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.textIndex, "part": part})
	}
	message.text.WriteString(delta)
	s.emit(w, map[string]any{
		"type": "response.output_text.delta", "item_id": message.id, "output_index": message.outputIndex,
		"content_index": message.textIndex, "delta": delta, "logprobs": []any{},
	})
}

func (s *copilotResponsesStreamState) writeRefusalDelta(w http.ResponseWriter, delta string) {
	message := s.ensureMessage(w)
	if message.refusalIndex < 0 {
		message.refusalIndex = message.nextContent
		message.nextContent++
		part := map[string]any{"type": "refusal", "refusal": ""}
		s.emit(w, map[string]any{"type": "response.content_part.added", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.refusalIndex, "part": part})
	}
	message.refusal.WriteString(delta)
	s.emit(w, map[string]any{
		"type": "response.refusal.delta", "item_id": message.id, "output_index": message.outputIndex,
		"content_index": message.refusalIndex, "delta": delta,
	})
}

func (s *copilotResponsesStreamState) ensureMessage(w http.ResponseWriter) *copilotStreamMessage {
	if s.message != nil {
		return s.message
	}
	s.message = &copilotStreamMessage{
		id: copilotMessageID(s.responseID), outputIndex: s.nextOutput, textIndex: -1, refusalIndex: -1,
	}
	s.nextOutput++
	item := map[string]any{"id": s.message.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
	s.emit(w, map[string]any{"type": "response.output_item.added", "output_index": s.message.outputIndex, "item": item})
	return s.message
}

func (s *copilotResponsesStreamState) writeToolDeltas(w http.ResponseWriter, calls []any) {
	for _, rawCall := range calls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			continue
		}
		index := int(responseevent.Number(call["index"]))
		tool := s.tools[index]
		if tool == nil {
			tool = &copilotStreamTool{id: fmt.Sprintf("call_%d", index), outputIndex: s.nextOutput}
			s.tools[index] = tool
			s.nextOutput++
		}
		if id, _ := call["id"].(string); id != "" {
			tool.id = id
		}
		function, _ := call["function"].(map[string]any)
		if name, _ := function["name"].(string); name != "" {
			tool.name.WriteString(name)
		}
		if arguments, _ := function["arguments"].(string); arguments != "" {
			s.addTool(w, tool)
			tool.arguments.WriteString(arguments)
			if !tool.custom {
				s.emit(w, map[string]any{
					"type": "response.function_call_arguments.delta", "item_id": tool.id,
					"output_index": tool.outputIndex, "delta": arguments,
				})
			}
		}
	}
}

func (s *copilotResponsesStreamState) addTool(w http.ResponseWriter, tool *copilotStreamTool) {
	if tool.added {
		return
	}
	tool.added = true
	upstreamName := tool.name.String()
	info, mapped := s.copilotTools[upstreamName]
	if mapped {
		tool.responseName = info.Name
		tool.namespace = info.Namespace
		tool.custom = info.Custom
	} else {
		tool.responseName = upstreamName
	}
	item := responseFunctionCall(tool.id, tool.responseName, "", "in_progress", tool.namespace)
	if tool.custom {
		item = responseCustomToolCall(tool.id, tool.responseName, "", "in_progress")
	}
	s.emit(w, map[string]any{"type": "response.output_item.added", "output_index": tool.outputIndex, "item": item})
}

func (s *copilotResponsesStreamState) finish(w http.ResponseWriter) bool {
	if !s.started || s.finishReason == "" {
		return false
	}
	output := make([]any, s.nextOutput)
	if s.message != nil {
		output[s.message.outputIndex] = s.finishMessage(w)
	}
	for outputIndex := 0; outputIndex < s.nextOutput; outputIndex++ {
		for _, tool := range s.tools {
			if tool.outputIndex != outputIndex {
				continue
			}
			s.addTool(w, tool)
			arguments := tool.arguments.String()
			item := responseFunctionCall(tool.id, tool.responseName, arguments, "completed", tool.namespace)
			if tool.custom {
				input := customToolInput(arguments)
				s.emit(w, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": tool.id, "output_index": tool.outputIndex, "delta": input})
				s.emit(w, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": tool.id, "output_index": tool.outputIndex, "input": input})
				item = responseCustomToolCall(tool.id, tool.responseName, input, "completed")
			} else {
				s.emit(w, map[string]any{"type": "response.function_call_arguments.done", "item_id": tool.id, "output_index": tool.outputIndex, "arguments": arguments})
			}
			s.emit(w, map[string]any{"type": "response.output_item.done", "output_index": tool.outputIndex, "item": item})
			output[tool.outputIndex] = item
			break
		}
	}
	status, incomplete := responseStatus(s.finishReason)
	if s.usage == nil {
		s.usage, _, _ = chatUsageToResponses(nil)
	}
	response := newCopilotResponse(s.responseID, s.model, s.created, status, output, s.usage)
	response["incomplete_details"] = incomplete
	s.emit(w, map[string]any{"type": "response." + status, "response": response})
	return true
}

func (s *copilotResponsesStreamState) finishMessage(w http.ResponseWriter) map[string]any {
	message := s.message
	content := make([]any, message.nextContent)
	if message.textIndex >= 0 {
		text := message.text.String()
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
		s.emit(w, map[string]any{"type": "response.output_text.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.textIndex, "text": text, "logprobs": []any{}})
		s.emit(w, map[string]any{"type": "response.content_part.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.textIndex, "part": part})
		content[message.textIndex] = part
	}
	if message.refusalIndex >= 0 {
		refusal := message.refusal.String()
		part := map[string]any{"type": "refusal", "refusal": refusal}
		s.emit(w, map[string]any{"type": "response.refusal.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.refusalIndex, "refusal": refusal})
		s.emit(w, map[string]any{"type": "response.content_part.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.refusalIndex, "part": part})
		content[message.refusalIndex] = part
	}
	item := map[string]any{"id": message.id, "type": "message", "status": "completed", "role": "assistant", "content": content}
	s.emit(w, map[string]any{"type": "response.output_item.done", "output_index": message.outputIndex, "item": item})
	return item
}

func (s *copilotResponsesStreamState) emit(w http.ResponseWriter, event map[string]any) {
	event["sequence_number"] = s.sequence
	s.sequence++
	writeSSE(w, event)
}
