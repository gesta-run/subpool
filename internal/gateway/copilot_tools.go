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
