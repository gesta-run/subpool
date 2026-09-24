package responseevent

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
)

func SSEData(line []byte) []byte {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "data:") {
		return nil
	}
	return []byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
}

func Usage(data []byte) (int64, int64) {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return 0, 0
	}
	usage, _ := value["usage"].(map[string]any)
	if response, ok := value["response"].(map[string]any); ok {
		if nested, ok := response["usage"].(map[string]any); ok {
			usage = nested
		}
	}
	if usage == nil {
		return 0, 0
	}
	input := Number(usage["input_tokens"])
	if input == 0 {
		input = Number(usage["prompt_tokens"])
	}
	output := Number(usage["output_tokens"])
	if output == 0 {
		output = Number(usage["completion_tokens"])
	}
	return input, output
}

func ResponseID(data []byte) string {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	if id, ok := value["response_id"].(string); ok {
		return id
	}
	if id, ok := value["id"].(string); ok {
		return id
	}
	if response, ok := value["response"].(map[string]any); ok {
		id, _ := response["id"].(string)
		return id
	}
	return ""
}

func SessionHash(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func Number(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case json.Number:
		number, _ := typed.Int64()
		return number
	default:
		return 0
	}
}
