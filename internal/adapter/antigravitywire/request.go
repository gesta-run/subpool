package antigravitywire

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type requestBuilder struct {
	scope, model string
	signatures   *Signatures
	contents     []any
	system       []any
	calls        map[string]string
	pending      map[string]bool
	assistant    assistantHistory
}

type assistantHistory struct {
	parts []any
	calls []historyCall
}

type historyCall struct {
	part   map[string]any
	prefix []any
}

func Request(raw []byte, scope, model string, signatures *Signatures) ([]byte, error) {
	var chat map[string]any
	if err := json.Unmarshal(raw, &chat); err != nil {
		return nil, fmt.Errorf("invalid JSON request")
	}
	if err := validateControls(chat); err != nil {
		return nil, err
	}
	b := requestBuilder{scope: scope, model: model, signatures: signatures, calls: map[string]string{}, pending: map[string]bool{}}
	messages, ok := chat["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, fmt.Errorf("messages must be a non-empty array")
	}
	for _, value := range messages {
		message, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("message entries must be objects")
		}
		if err := b.message(message); err != nil {
			return nil, err
		}
	}
	if err := b.flushAssistant(); err != nil {
		return nil, err
	}
	if len(b.pending) > 0 {
		return nil, fmt.Errorf("each assistant tool call requires a matching tool result")
	}
	if len(b.contents) == 0 {
		return nil, fmt.Errorf("at least one user message is required")
	}
	body := map[string]any{"contents": b.contents}
	if len(b.system) > 0 {
		body["systemInstruction"] = map[string]any{"parts": b.system}
	}
	config, err := generationConfig(chat, model)
	if err != nil {
		return nil, err
	}
	if len(config) > 0 {
		body["generationConfig"] = config
	}
	if err := addTools(body, chat); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

func (b *requestBuilder) append(role string, parts []any) {
	if len(parts) == 0 {
		return
	}
	if len(b.contents) > 0 {
		last := b.contents[len(b.contents)-1].(map[string]any)
		if last["role"] == role {
			last["parts"] = append(last["parts"].([]any), parts...)
			return
		}
	}
	b.contents = append(b.contents, map[string]any{"role": role, "parts": parts})
}

func (b *requestBuilder) message(message map[string]any) error {
	role, _ := message["role"].(string)
	if role != "assistant" {
		if err := b.flushAssistant(); err != nil {
			return err
		}
	}
	if role == "tool" {
		id, _ := message["tool_call_id"].(string)
		if !b.pending[id] {
			return fmt.Errorf("tool result has no matching pending call")
		}
		delete(b.pending, id)
		output, ok := message["content"].(string)
		if !ok {
			return fmt.Errorf("tool results currently require text content")
		}
		b.append("user", []any{map[string]any{"functionResponse": map[string]any{"id": id, "name": b.calls[id], "response": map[string]any{"output": output}}}})
		return nil
	}
	if len(b.pending) > 0 && (role != "assistant" || len(b.assistant.calls) == 0) {
		return fmt.Errorf("tool results must immediately follow their assistant calls")
	}
	if role != "user" && role != "assistant" && role != "system" && role != "developer" {
		return fmt.Errorf("unsupported message role %q", role)
	}
	parts, err := contentParts(message["content"])
	if err != nil {
		return err
	}
	if role == "system" || role == "developer" {
		for _, part := range parts {
			if part.(map[string]any)["text"] == nil {
				return fmt.Errorf("system instructions require text")
			}
		}
		b.system = append(b.system, parts...)
		return nil
	}
	if role == "assistant" {
		b.assistant.parts = append(b.assistant.parts, parts...)
	}
	if calls, ok := message["tool_calls"].([]any); ok {
		if role != "assistant" {
			return fmt.Errorf("only assistant messages can contain tool calls")
		}
		for _, value := range calls {
			part, err := b.call(value)
			if err != nil {
				return err
			}
			b.assistant.calls = append(b.assistant.calls, part)
			if !strings.HasPrefix(strings.ToLower(b.model), "claude-") {
				b.assistant.parts = append(b.assistant.parts, part.prefix...)
			}
			b.assistant.parts = append(b.assistant.parts, part.part)
		}
	}
	if role == "assistant" {
		return nil
	}
	b.append(role, parts)
	return nil
}

func (b *requestBuilder) call(value any) (historyCall, error) {
	call, ok := value.(map[string]any)
	if !ok {
		return historyCall{}, fmt.Errorf("invalid assistant tool call")
	}
	id, _ := call["id"].(string)
	function, _ := call["function"].(map[string]any)
	name, _ := function["name"].(string)
	arguments, _ := function["arguments"].(string)
	var args map[string]any
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	if id == "" || name == "" || !json.Valid([]byte(arguments)) || decoder.Decode(&args) != nil || args == nil {
		return historyCall{}, fmt.Errorf("tool call requires id, name and object arguments")
	}
	if _, exists := b.calls[id]; exists {
		return historyCall{}, fmt.Errorf("duplicate tool call id")
	}
	part := map[string]any{"functionCall": map[string]any{"id": id, "name": name, "args": args}}
	var parts []any
	if sig, prefix, exists := b.signatures.History(b.scope, b.model, id, name, args); exists {
		if sig != "" {
			part["thoughtSignature"] = sig
		}
		if len(prefix) > 0 {
			if err := json.NewDecoder(bytes.NewReader(prefix)).Decode(&parts); err != nil {
				return historyCall{}, fmt.Errorf("invalid saved Antigravity native assistant state")
			}
		}
	} else if requiresSignature(b.model) {
		return historyCall{}, fmt.Errorf("Antigravity tool thought signature is unavailable for this account/model; start a new conversation")
	}
	b.calls[id], b.pending[id] = name, true
	return historyCall{part: part, prefix: parts}, nil
}

func (b *requestBuilder) flushAssistant() error {
	history := b.assistant
	b.assistant = assistantHistory{}
	parts := history.parts
	if strings.HasPrefix(strings.ToLower(b.model), "claude-") && len(history.calls) > 0 {
		var err error
		parts, err = history.nativeParts()
		if err != nil {
			return err
		}
	}
	b.append("model", parts)
	return nil
}

func (h assistantHistory) nativeParts() ([]any, error) {
	var visible strings.Builder
	for _, value := range h.parts {
		part := value.(map[string]any)
		if part["functionCall"] != nil {
			continue
		}
		text, ok := part["text"].(string)
		if !ok {
			return nil, fmt.Errorf("Antigravity native assistant history requires text content")
		}
		visible.WriteString(text)
	}
	remaining := visible.String()
	var parts []any
	for _, call := range h.calls {
		for _, value := range call.prefix {
			part, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid saved Antigravity native assistant state")
			}
			if text, ok := part["text"].(string); ok && part["thought"] != true {
				if !strings.HasPrefix(remaining, text) {
					return nil, fmt.Errorf("Antigravity native assistant history was modified; start a new conversation")
				}
				remaining = strings.TrimPrefix(remaining, text)
			}
			parts = append(parts, part)
		}
		parts = append(parts, call.part)
	}
	if remaining != "" {
		parts = append(parts, map[string]any{"text": remaining})
	}
	return parts, nil
}

func requiresSignature(model string) bool {
	model = strings.ToLower(model)
	version := strings.SplitN(strings.TrimPrefix(model, "gemini-"), ".", 2)[0]
	version = strings.SplitN(version, "-", 2)[0]
	major, _ := strconv.Atoi(version)
	return strings.HasPrefix(model, "gemini-") && major >= 3 || strings.HasPrefix(model, "claude-") || strings.Contains(model, "thinking")
}

func contentParts(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return nil, nil
		}
		return []any{map[string]any{"text": text}}, nil
	}
	content, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("message content must be text or an array")
	}
	parts := make([]any, 0, len(content))
	for _, value := range content {
		part, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid content part")
		}
		switch part["type"] {
		case "text":
			text, ok := part["text"].(string)
			if !ok {
				return nil, fmt.Errorf("text content requires text")
			}
			parts = append(parts, map[string]any{"text": text})
		case "image_url":
			image, _ := part["image_url"].(map[string]any)
			uri, _ := image["url"].(string)
			mime, data, ok := strings.Cut(strings.TrimPrefix(uri, "data:"), ";base64,")
			if !strings.HasPrefix(uri, "data:image/") || !ok {
				return nil, fmt.Errorf("Antigravity images require base64 data URLs; remote URLs are not fetched")
			}
			if _, err := base64.StdEncoding.DecodeString(data); err != nil {
				return nil, fmt.Errorf("invalid base64 image")
			}
			parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mime, "data": data}})
		default:
			return nil, fmt.Errorf("unsupported Antigravity content type %q", part["type"])
		}
	}
	return parts, nil
}
