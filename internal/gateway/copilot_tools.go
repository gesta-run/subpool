package gateway

import "fmt"

const maxCopilotToolNameLength = 64

type copilotToolInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Custom    bool   `json:"custom,omitempty"`
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
