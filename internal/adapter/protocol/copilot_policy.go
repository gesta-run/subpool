package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gesta-run/subpool/internal/jsonobject"
	"regexp"
	"strconv"
	"unicode/utf8"
)

const MaxCopilotToolNameLength = 64

type ToolInfo struct {
	Name         string         `json:"name"`
	Namespace    string         `json:"namespace,omitempty"`
	Custom       bool           `json:"custom,omitempty"`
	InputPattern *regexp.Regexp `json:"-"`
}

// isCopilotOmittableTool lists Responses tools that Copilot Chat Completions cannot represent.
func isCopilotOmittableTool(toolType string) bool {
	switch toolType {
	case "web_search", "web_search_preview", "file_search", "mcp",
		"shell", "local_shell", "computer", "computer_use_preview",
		"image_generation", "code_interpreter":
		return true
	default:
		return false
	}
}

func responseToolsWithAdditional(value any, hasTools bool, input any) ([]any, error) {
	var tools []any
	if hasTools {
		declared, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("tools must be an array")
		}
		tools = append(tools, declared...)
	}
	items, ok := input.([]any)
	if !ok {
		return tools, nil
	}
	for _, rawItem := range items {
		item, valid := rawItem.(map[string]any)
		if !valid || item["type"] != "additional_tools" {
			continue
		}
		role, _ := item["role"].(string)
		if role != "developer" {
			return nil, fmt.Errorf("additional_tools requires developer role")
		}
		additional, valid := item["tools"].([]any)
		if !valid {
			return nil, fmt.Errorf("additional_tools requires tools")
		}
		tools = append(tools, additional...)
	}
	return tools, nil
}

func responseCustomToolToChat(tool map[string]any, name string) (map[string]any, error) {
	originalName, _ := tool["name"].(string)
	if originalName == "" {
		return nil, fmt.Errorf("custom tool requires name")
	}
	if name == "" {
		name = originalName
	}
	function := map[string]any{"name": name}
	if description, ok := tool["description"].(string); ok && description != "" {
		function["description"] = description
	}
	input, err := copilotCustomInputSchema(tool["format"])
	if err != nil {
		return nil, err
	}
	function["parameters"] = map[string]any{
		"type": "object", "properties": map[string]any{"input": input},
		"required": []string{"input"}, "additionalProperties": false,
	}
	return function, nil
}

func copilotCustomInputSchema(value any) (map[string]any, error) {
	input := map[string]any{"type": "string"}
	if value == nil {
		return input, nil
	}
	format, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("custom tool format must be an object")
	}
	switch format["type"] {
	case "text":
		return input, nil
	case "grammar":
		definition, _ := format["definition"].(string)
		if definition == "" {
			return nil, fmt.Errorf("custom tool grammar requires a definition")
		}
		syntax, _ := format["syntax"].(string)
		switch syntax {
		case "regex":
			if _, err := compileCopilotRegexGrammar(definition); err != nil {
				return nil, fmt.Errorf("custom tool regex is not supported: %w", err)
			}
			input["pattern"] = definition
		case "lark":
			// Function wrapping cannot enforce CFG sampling. Retain the grammar
			// as explicit guidance; the client tool still validates its input.
		default:
			return nil, fmt.Errorf("custom tool grammar syntax %q is not supported", syntax)
		}
		input["description"] = "Return input conforming to this " + syntax + " grammar, without Markdown fences:\n" + definition
		return input, nil
	default:
		return nil, fmt.Errorf("custom tool format type is not supported")
	}
}

func compileCopilotRegexGrammar(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("\\A(?:" + pattern + ")\\z")
}

func responseToolChoiceToChat(value any, copilotTools map[string]ToolInfo, allowToolFallback bool) (any, error) {
	if choice, ok := value.(string); ok {
		switch choice {
		case "auto", "none", "required":
			return choice, nil
		default:
			return nil, fmt.Errorf("tool_choice %q is not supported for GitHub Copilot", choice)
		}
	}
	choice, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool_choice is not supported for GitHub Copilot")
	}
	choiceType, _ := choice["type"].(string)
	if allowToolFallback && (choiceType == "tool_search" || isCopilotOmittableTool(choiceType)) {
		return nil, nil
	}
	if choiceType != "function" && choiceType != "custom" {
		return nil, fmt.Errorf("tool_choice is not supported for GitHub Copilot")
	}
	name, _ := choice["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("function tool_choice requires name")
	}
	namespace, _ := choice["namespace"].(string)
	name = CopilotToolName(name, namespace, copilotTools)
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
}

func copilotOmitsNullReasoningContent(model string) bool {
	return model == "grok-4.7" || model == "grok-4.6"
}

func copilotUsesStringAssistantHistory(model string) bool {
	return model == "claude-sonnet-5.5" || model == "claude-haiku-4.5"
}

type RequestError struct {
	Code    string
	Message string
}

func (e *RequestError) Error() string { return e.Message }

func ValidateCopilotRequest(kind, model string, raw []byte) error {
	if kind != "responses" && kind != "chat_completions" {
		return nil
	}
	if model == "claude-sonnet-5.5" {
		encoded, exists := jsonobject.Lookup(raw, "tool_choice")
		if exists {
			var choice any
			if err := json.Unmarshal(encoded, &choice); err != nil {
				return fmt.Errorf("invalid tool_choice")
			}
			if copilotForcesFunctionChoice(choice) {
				return &RequestError{Code: "unsupported_tool_choice", Message: "GitHub Copilot claude-sonnet-5.5 does not support required or named function/custom tool_choice; use auto or none, or select a model that supports forced tool calls"}
			}
		}
	}
	if model == "gemini-3.8-flash" {
		effort, _ := jsonobject.Lookup(raw, "reasoning_effort")
		if kind == "responses" {
			reasoning, _ := jsonobject.Lookup(raw, "reasoning")
			effort, _ = jsonobject.Lookup(reasoning, "effort")
		}
		var value string
		if json.Unmarshal(effort, &value) == nil && value == "xhigh" {
			return &RequestError{Code: "invalid_reasoning_effort", Message: "GitHub Copilot gemini-3.8-flash does not support xhigh reasoning effort; use a supported effort such as high or omit it"}
		}
	}
	return nil
}

func copilotForcesFunctionChoice(value any) bool {
	if choice, ok := value.(string); ok {
		return choice == "required"
	}
	choice, ok := value.(map[string]any)
	if !ok {
		return false
	}
	kind, _ := choice["type"].(string)
	return kind == "function" || kind == "custom" || (kind == "allowed_tools" && choice["mode"] == "required")
}

func NormalizeCopilotChatHistory(raw []byte, model string) ([]byte, error) {
	if !copilotUsesStringAssistantHistory(model) {
		return raw, nil
	}
	object, err := jsonobject.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON request")
	}
	return object.BytesWithValue("messages", func(output *bytes.Buffer, value []byte) error {
		value = bytes.TrimSpace(value)
		if len(value) == 0 || value[0] != '[' {
			output.Write(value)
			return nil
		}
		output.WriteByte('[')
		written := false
		err := jsonobject.VisitArray(value, func(item []byte) error {
			if written {
				output.WriteByte(',')
			}
			written = true
			return writeCopilotStringAssistantHistory(output, item)
		})
		output.WriteByte(']')
		return err
	})
}

func writeCopilotStringAssistantHistory(output *bytes.Buffer, raw []byte) error {
	if encodedType, exists := jsonobject.Lookup(raw, "type"); exists {
		var kind string
		if json.Unmarshal(encodedType, &kind) != nil || (kind != "" && kind != "message") {
			output.Write(raw)
			return nil
		}
	}
	encodedRole, _ := jsonobject.Lookup(raw, "role")
	var role string
	if json.Unmarshal(encodedRole, &role) != nil || role != "assistant" {
		output.Write(raw)
		return nil
	}
	content, _ := jsonobject.Lookup(raw, "content")
	if !copilotPlainAssistantText(content) {
		output.Write(raw)
		return nil
	}
	item, err := jsonobject.Parse(raw)
	if err != nil {
		return err
	}
	return item.AppendWithValue(output, "content", writeCopilotAssistantText)
}

// Keep rich content intact; only empty output metadata may be discarded.
func copilotPlainAssistantText(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	plain, count := true, 0
	err := jsonobject.VisitArray(raw, func(part []byte) error {
		count++
		if !plain {
			return nil
		}
		plain = copilotPlainAssistantPart(part)
		return nil
	})
	return err == nil && plain && count > 0
}

func copilotPlainAssistantPart(raw []byte) bool {
	encodedType, _ := jsonobject.Lookup(raw, "type")
	var kind string
	if json.Unmarshal(encodedType, &kind) != nil || (kind != "text" && kind != "input_text" && kind != "output_text") {
		return false
	}
	for _, key := range []string{"annotations", "logprobs"} {
		metadata, exists := jsonobject.Lookup(raw, key)
		if exists && !copilotEmptyOutputMetadata(metadata) {
			return false
		}
	}
	part, err := jsonobject.Parse(raw)
	if err != nil || !part.HasOnlyFields("type", "text", "annotations", "logprobs") {
		return false
	}
	text, _ := part.Value("text")
	return isCopilotJSONString(text)
}

// Validate all parts before writing; rich content must never be partly flattened.
// Copy JSON string interiors into the final buffer instead of decoding large text.
func writeCopilotAssistantText(output *bytes.Buffer, raw []byte) error {
	output.WriteByte('"')
	err := jsonobject.VisitArray(raw, func(part []byte) error {
		text, _ := jsonobject.Lookup(part, "text")
		appendCopilotJSONStringText(output, text[1:len(text)-1])
		return nil
	})
	output.WriteByte('"')
	return err
}

// Match per-part JSON decoding without allowing broken Unicode sequences in
// adjacent parts to combine into a different character after concatenation.
func appendCopilotJSONStringText(output *bytes.Buffer, raw []byte) {
	start := 0
	for index := 0; index < len(raw); {
		if raw[index] == '\\' {
			value, unicode := copilotUnicodeEscape(raw[index:])
			if !unicode {
				index += 2
				continue
			}
			if value >= 0xd800 && value <= 0xdbff {
				next, ok := copilotUnicodeEscape(raw[index+6:])
				if ok && next >= 0xdc00 && next <= 0xdfff {
					index += 12
					continue
				}
			} else if value < 0xdc00 || value > 0xdfff {
				index += 6
				continue
			}
			output.Write(raw[start:index])
			output.WriteString(`\ufffd`)
			index += 6
			start = index
		} else if raw[index] >= utf8.RuneSelf {
			_, size := utf8.DecodeRune(raw[index:])
			if size == 1 {
				output.Write(raw[start:index])
				output.WriteRune(utf8.RuneError)
				start = index + 1
			}
			index += size
		} else {
			index++
		}
	}
	output.Write(raw[start:])
}

func copilotUnicodeEscape(raw []byte) (uint64, bool) {
	if len(raw) < 6 || raw[0] != '\\' || raw[1] != 'u' {
		return 0, false
	}
	value, err := strconv.ParseUint(string(raw[2:6]), 16, 16)
	return value, err == nil
}

func copilotEmptyOutputMetadata(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return bytes.Equal(raw, []byte("null")) || len(raw) >= 2 && raw[0] == '[' && raw[len(raw)-1] == ']' && len(bytes.TrimSpace(raw[1:len(raw)-1])) == 0
}

func isCopilotJSONString(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"'
}
