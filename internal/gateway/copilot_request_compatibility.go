package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/jsonobject"
)

func copilotOmitsNullReasoningContent(model string) bool {
	return model == "grok-4.7" || model == "grok-4.6"
}

func copilotUsesStringAssistantHistory(model string) bool {
	return model == "claude-sonnet-5.5" || model == "claude-haiku-4.5"
}

type copilotRequestError struct {
	code    string
	message string
}

func (e *copilotRequestError) Error() string { return e.message }

func validateCopilotRequest(kind, model string, raw []byte) error {
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
				return &copilotRequestError{code: "unsupported_tool_choice", message: "GitHub Copilot claude-sonnet-5.5 does not support required or named function/custom tool_choice; use auto or none, or select a model that supports forced tool calls"}
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
			return &copilotRequestError{code: "invalid_reasoning_effort", message: "GitHub Copilot gemini-3.8-flash does not support xhigh reasoning effort; use a supported effort such as high or omit it"}
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

func copilotRequestFailure(poolProvider string, err error) (*http.Response, retryReason, bool) {
	if poolProvider == domain.ProviderMixed {
		return nil, retryUnsupported, false
	}
	var invalid *copilotRequestError
	if errors.As(err, &invalid) {
		return openAIErrorResponse(http.StatusBadRequest, invalid.message, invalid.code), "", true
	}
	return chatConversionFailure(poolProvider, err)
}

func normalizeCopilotChatHistory(raw []byte, model string) ([]byte, error) {
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
