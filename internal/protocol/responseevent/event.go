package responseevent

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
)

type LimitKind uint8

const (
	LimitNone LimitKind = iota
	LimitTemporary
	LimitQuota
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

func ClassifyLimit(data []byte) LimitKind {
	var event struct {
		Type     string          `json:"type"`
		Code     string          `json:"code"`
		Message  string          `json:"message"`
		Error    json.RawMessage `json:"error"`
		Response struct {
			Error json.RawMessage `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil {
		return LimitNone
	}
	errorType, errorCode, errorMessage, hasError := limitError(event.Error)
	responseType, responseCode, responseMessage, _ := limitError(event.Response.Error)
	typedError := event.Type == "error" || event.Type == "response.failed"
	rawError := event.Type == "" && (event.Code != "" || event.Message != "" || hasError)
	if !typedError && !rawError {
		return LimitNone
	}
	message := strings.ToLower(event.Message + " " + errorMessage + " " + responseMessage)
	for _, phrase := range []string{"hit your usage limit", "reached your usage limit", "usage limit reached", "usage limit has been reached", "quota exceeded", "quota exhausted", "insufficient quota"} {
		if strings.Contains(message, phrase) {
			return LimitQuota
		}
	}
	for _, signal := range []string{event.Code, errorType, errorCode, responseType, responseCode} {
		normalized := strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(signal))
		if strings.Contains(normalized, "usage_limit") || strings.Contains(normalized, "quota") {
			return LimitQuota
		}
		if strings.Contains(normalized, "rate_limit") {
			return LimitTemporary
		}
	}
	if strings.Contains(message, "rate limit exceeded") || strings.Contains(message, "too many requests") {
		return LimitTemporary
	}
	return LimitNone
}

func limitError(raw json.RawMessage) (string, string, string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", "", false
	}
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return "", "", message, message != ""
	}
	var value struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return "", "", "", false
	}
	return value.Type, value.Code, value.Message, value.Type != "" || value.Code != "" || value.Message != ""
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
