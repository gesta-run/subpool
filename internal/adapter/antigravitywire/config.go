package antigravitywire

import (
	"fmt"
	"regexp"
	"strings"
)

var functionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]{0,63}$`)

func validateControls(chat map[string]any) error {
	if parallel, ok := chat["parallel_tool_calls"].(bool); ok && !parallel {
		return fmt.Errorf("Antigravity cannot enforce parallel_tool_calls=false")
	}
	for _, field := range []string{"audio", "modalities", "prediction", "logprobs", "top_logprobs", "logit_bias", "frequency_penalty", "presence_penalty"} {
		if value := chat[field]; value != nil && value != false && value != float64(0) {
			return fmt.Errorf("Antigravity does not support parameter %q", field)
		}
	}
	if n, exists := chat["n"]; exists && n != float64(1) {
		return fmt.Errorf("Antigravity supports only n=1")
	}
	return nil
}

func generationConfig(chat map[string]any, model string) (map[string]any, error) {
	config := map[string]any{}
	for source, target := range map[string]string{"temperature": "temperature", "top_p": "topP", "seed": "seed", "max_tokens": "maxOutputTokens", "max_completion_tokens": "maxOutputTokens", "stop": "stopSequences"} {
		if value, ok := chat[source]; ok {
			config[target] = value
		}
	}
	// max_completion_tokens takes precedence, independent of map iteration order.
	if value, ok := chat["max_completion_tokens"]; ok {
		config["maxOutputTokens"] = value
	}
	if stop, ok := config["stopSequences"].(string); ok {
		config["stopSequences"] = []string{stop}
	}
	if effort, ok := chat["reasoning_effort"].(string); ok && effort != "" {
		thinking, err := thinkingConfig(effort, model)
		if err != nil {
			return nil, err
		}
		config["thinkingConfig"] = thinking
		if err := validateThinkingLimit(config, model, thinking); err != nil {
			return nil, err
		}
	}
	if format, ok := chat["response_format"].(map[string]any); ok {
		switch format["type"] {
		case "text", nil:
		case "json_object":
			config["responseMimeType"] = "application/json"
		case "json_schema":
			schema, _ := format["json_schema"].(map[string]any)
			if schema["schema"] == nil {
				return nil, fmt.Errorf("JSON response format requires a schema")
			}
			config["responseMimeType"], config["responseJsonSchema"] = "application/json", schema["schema"]
		default:
			return nil, fmt.Errorf("unsupported response format")
		}
	}
	return config, nil
}

func thinkingConfig(effort, model string) (map[string]any, error) {
	budgets := map[string]int{"none": 0, "minimal": 512, "low": 1024, "medium": 8192, "high": 16384, "xhigh": 24576}
	budget, ok := budgets[effort]
	if !ok {
		return nil, fmt.Errorf("unsupported reasoning effort %q", effort)
	}
	model = strings.ToLower(model)
	if strings.HasPrefix(model, "gemini-3") {
		level := effort
		if level == "none" || level == "minimal" {
			level = "low"
		}
		if level == "xhigh" {
			level = "high"
		}
		if level == "medium" && strings.Contains(model, "pro") {
			level = "high"
		}
		return map[string]any{"thinkingLevel": level}, nil
	}
	if strings.HasPrefix(model, "claude-") && budget > 0 {
		return map[string]any{"thinkingBudget": max(1024, budget), "includeThoughts": true}, nil
	}
	if strings.HasPrefix(model, "gemini-2.5") {
		return map[string]any{"thinkingBudget": budget}, nil
	}
	return nil, fmt.Errorf("reasoning controls are not mapped for this Antigravity model")
}

func validateThinkingLimit(config map[string]any, model string, thinking map[string]any) error {
	if !strings.HasPrefix(strings.ToLower(model), "claude-") {
		return nil
	}
	budget, _ := thinking["thinkingBudget"].(int)
	if budget <= 0 {
		return nil
	}
	maximum, exists := config["maxOutputTokens"]
	if !exists {
		config["maxOutputTokens"] = 64000
		return nil
	}
	limit, ok := maximum.(float64)
	if !ok || limit <= float64(budget) {
		return fmt.Errorf("Antigravity Claude thinking budget %d requires an output token limit greater than %d; increase max_completion_tokens (or max_output_tokens for Responses) or lower reasoning_effort", budget, budget)
	}
	return nil
}

func addTools(body, chat map[string]any) error {
	var declarations []any
	declared := map[string]bool{}
	if tools, ok := chat["tools"].([]any); ok {
		for _, value := range tools {
			tool, ok := value.(map[string]any)
			if !ok || tool["type"] != "function" {
				return fmt.Errorf("Antigravity supports function tools only")
			}
			function, _ := tool["function"].(map[string]any)
			name, _ := function["name"].(string)
			if !functionNamePattern.MatchString(name) || declared[name] {
				return fmt.Errorf("Antigravity function names must be unique, start with a letter or underscore, and contain at most 64 supported characters")
			}
			declared[name] = true
			declaration := map[string]any{"name": name}
			if value := function["description"]; value != nil {
				declaration["description"] = value
			}
			if value := function["parameters"]; value != nil {
				declaration["parametersJsonSchema"] = value
			}
			declarations = append(declarations, declaration)
		}
	}
	if len(declarations) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
	}
	mode, names := "AUTO", []string(nil)
	if choice, ok := chat["tool_choice"].(string); ok {
		switch choice {
		case "auto":
		case "none":
			mode = "NONE"
		case "required":
			mode = "ANY"
		default:
			return fmt.Errorf("unsupported tool_choice")
		}
	} else if choice, ok := chat["tool_choice"].(map[string]any); ok {
		function, _ := choice["function"].(map[string]any)
		name, _ := function["name"].(string)
		if choice["type"] != "function" || !declared[name] {
			return fmt.Errorf("invalid function tool_choice")
		}
		mode, names = "ANY", []string{name}
	}
	if mode == "ANY" && len(declarations) == 0 {
		return fmt.Errorf("required tool_choice requires function declarations")
	}
	if len(declarations) == 0 && mode == "AUTO" {
		mode = "VALIDATED"
	}
	functionConfig := map[string]any{"mode": mode}
	if len(names) > 0 {
		functionConfig["allowedFunctionNames"] = names
	}
	body["toolConfig"] = map[string]any{"functionCallingConfig": functionConfig}
	return nil
}
