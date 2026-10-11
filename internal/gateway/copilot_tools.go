package gateway

import (
	"fmt"
	"regexp"
)

const maxCopilotToolNameLength = 64

type copilotToolInfo struct {
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

func responseToolChoiceToChat(value any, copilotTools map[string]copilotToolInfo, allowToolFallback bool) (any, error) {
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
	name = copilotToolName(name, namespace, copilotTools)
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
}
