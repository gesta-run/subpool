package gateway

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gesta-run/subpool/internal/jsonobject"
)

// An advertised Responses endpoint does not imply support for all OpenAI tools.
// In particular, Copilot's Grok endpoint rejects hosted tools and tool namespaces.
func normalizeCopilotNativeResponses(raw []byte, model string, allowToolFallback bool) ([]byte, map[string]copilotToolInfo, error) {
	object, err := jsonobject.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid JSON request")
	}
	tools, err := copilotNativeDeclaredTools(object)
	if err != nil {
		return nil, nil, err
	}
	omitHosted := strings.HasPrefix(strings.ToLower(model), "grok-")
	nativeTools, mappings, err := copilotNativeTools(tools, omitHosted, allowToolFallback)
	if err != nil {
		return nil, nil, err
	}
	if len(nativeTools) == 0 {
		object.Delete("tools")
	} else if err := setCopilotNativeJSON(object, "tools", nativeTools); err != nil {
		return nil, nil, err
	}
	if err := normalizeCopilotNativeChoice(object, len(nativeTools), omitHosted, allowToolFallback); err != nil {
		return nil, nil, err
	}
	_ = object.Set("stream", []byte("true"))
	body, err := object.BytesWithValue("input", func(output *bytes.Buffer, raw []byte) error {
		return writeCopilotNativeInput(output, raw)
	})
	return body, mappings, err
}

func copilotNativeDeclaredTools(object *jsonobject.Object) ([]any, error) {
	var tools []any
	if raw, exists := object.Value("tools"); exists {
		var value any
		if err := decodeCopilotNativeJSON(raw, &value); err != nil {
			return nil, err
		}
		var ok bool
		if tools, ok = value.([]any); !ok {
			return nil, fmt.Errorf("tools must be an array")
		}
	}
	raw, _ := object.Value("input")
	if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '[' {
		return tools, nil
	}
	err := jsonobject.VisitArray(raw, func(item []byte) error {
		if nativeInputType(item) != "additional_tools" {
			return nil
		}
		var additional map[string]any
		if err := decodeCopilotNativeJSON(item, &additional); err != nil {
			return err
		}
		declared, err := responseToolsWithAdditional(nil, false, []any{additional})
		tools = append(tools, declared...)
		return err
	})
	return tools, err
}

func copilotNativeTools(tools []any, omitHosted, allowToolFallback bool) ([]any, map[string]copilotToolInfo, error) {
	var callable, hosted []any
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("tools entries must be objects")
		}
		switch kind, _ := tool["type"].(string); kind {
		case "function", "custom", "namespace", "tool_search":
			callable = append(callable, tool)
		default:
			if omitHosted && isCopilotOmittableTool(kind) {
				if !allowToolFallback {
					return nil, nil, fmt.Errorf("tool type %q is not supported by this GitHub Copilot model", kind)
				}
				continue
			}
			hosted = append(hosted, tool)
		}
	}
	flat, mappings, err := responsesToolsToChat(callable, allowToolFallback)
	if err != nil {
		return nil, nil, err
	}
	mappings, err = stableCopilotNativeToolNames(flat, mappings)
	if err != nil {
		return nil, nil, err
	}
	nativeTools := make([]any, 0, len(flat)+len(hosted))
	for _, entry := range flat {
		function := entry.(map[string]any)["function"].(map[string]any)
		function["type"] = "function"
		nativeTools = append(nativeTools, function)
	}
	nativeTools = append(nativeTools, hosted...)
	return nativeTools, mappings, nil
}

func normalizeCopilotNativeChoice(object *jsonobject.Object, count int, omitHosted, allowToolFallback bool) error {
	if encoded, exists := object.Value("tool_choice"); exists {
		var choice any
		if err := decodeCopilotNativeJSON(encoded, &choice); err != nil {
			return err
		}
		if forced, ok := choice.(map[string]any); ok && omitHosted {
			if kind, _ := forced["type"].(string); isCopilotOmittableTool(kind) {
				return fmt.Errorf("forced tool_choice %q is not supported by this GitHub Copilot model", kind)
			}
		}
		choice, err := copilotNativeToolChoice(choice, omitHosted && allowToolFallback)
		if err != nil {
			return err
		}
		if count == 0 && choice != nil && choice != "auto" && choice != "none" {
			return fmt.Errorf("tool_choice has no supported tools")
		}
		if choice == nil || count == 0 && choice != "none" {
			object.Delete("tool_choice")
		} else if err := setCopilotNativeJSON(object, "tool_choice", choice); err != nil {
			return err
		}
	}
	return nil
}

func nativeInputType(raw []byte) string {
	encoded, _ := jsonobject.Lookup(raw, "type")
	switch string(encoded) {
	case `"additional_tools"`:
		return "additional_tools"
	case `"function_call"`:
		return "function_call"
	case `"custom_tool_call"`:
		return "custom_tool_call"
	case `"custom_tool_call_output"`:
		return "custom_tool_call_output"
	default:
		if bytes.IndexByte(encoded, '\\') < 0 {
			return ""
		}
	}
	var kind string
	_ = json.Unmarshal(encoded, &kind)
	return kind
}

func writeCopilotNativeInput(output *bytes.Buffer, raw []byte) error {
	if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '[' {
		output.Write(raw)
		return nil
	}
	output.WriteByte('[')
	written := false
	err := jsonobject.VisitArray(raw, func(rawItem []byte) error {
		kind := nativeInputType(rawItem)
		if kind == "additional_tools" {
			return nil
		}
		if written {
			output.WriteByte(',')
		}
		written = true
		if kind != "function_call" && kind != "custom_tool_call" && kind != "custom_tool_call_output" {
			output.Write(rawItem)
			return nil
		}
		return writeCopilotNativeToolHistory(output, rawItem, kind)
	})
	output.WriteByte(']')
	return err
}

func writeCopilotNativeToolHistory(output *bytes.Buffer, raw []byte, kind string) error {
	item, err := jsonobject.Parse(raw)
	if err != nil {
		output.Write(raw)
		return nil
	}
	if encoded, exists := item.Value("namespace"); exists && kind != "custom_tool_call_output" {
		var namespace, name string
		if json.Unmarshal(encoded, &namespace) == nil && namespace != "" {
			encodedName, _ := item.Value("name")
			_ = json.Unmarshal(encodedName, &name)
			_ = setCopilotNativeJSON(item, "name", nativeCopilotToolName(name, namespace))
			item.Delete("namespace")
		}
	}
	if kind == "custom_tool_call" {
		encoded, _ := item.Value("input")
		var text string
		if len(encoded) == 0 || bytes.TrimSpace(encoded)[0] != '"' || json.Unmarshal(encoded, &text) != nil {
			return fmt.Errorf("custom_tool_call requires input")
		}
		arguments, _ := json.Marshal(map[string]any{"input": text})
		_ = setCopilotNativeJSON(item, "type", "function_call")
		_ = setCopilotNativeJSON(item, "arguments", string(arguments))
		item.Delete("input")
	} else if kind == "custom_tool_call_output" {
		_ = setCopilotNativeJSON(item, "type", "function_call_output")
	}
	item.AppendTo(output)
	return nil
}

// Native previous_response_id history keeps upstream names across requests.
// Never change an alias to make room for a new declaration: reject collisions.
func stableCopilotNativeToolNames(flat []any, previous map[string]copilotToolInfo) (map[string]copilotToolInfo, error) {
	mappings := make(map[string]copilotToolInfo, len(previous))
	used := make(map[string]struct{}, len(flat))
	for _, entry := range flat {
		function := entry.(map[string]any)["function"].(map[string]any)
		name, _ := function["name"].(string)
		if info := previous[name]; info.Namespace == "" {
			if _, exists := used[name]; exists {
				return nil, fmt.Errorf("tools declare duplicate name %q", name)
			}
			used[name] = struct{}{}
		}
	}
	for _, entry := range flat {
		function := entry.(map[string]any)["function"].(map[string]any)
		name, _ := function["name"].(string)
		info, mapped := previous[name]
		if info.Namespace != "" {
			name = stableCopilotNativeToolName(info.Namespace, info.Name)
			if _, exists := used[name]; exists {
				return nil, fmt.Errorf("tool name %q conflicts with a stable namespace alias", name)
			}
			used[name] = struct{}{}
			function["name"] = name
		}
		if mapped {
			if info.Custom {
				parameters, _ := function["parameters"].(map[string]any)
				properties, _ := parameters["properties"].(map[string]any)
				input, _ := properties["input"].(map[string]any)
				if pattern, ok := input["pattern"].(string); ok {
					var err error
					info.InputPattern, err = compileCopilotRegexGrammar(pattern)
					if err != nil {
						return nil, err
					}
				}
			}
			mappings[name] = info
		}
	}
	return mappings, nil
}

func stableCopilotNativeToolName(namespace, name string) string {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(namespace+"\x00"+name)))
	var prefix strings.Builder
	for _, char := range namespace + "__" + name {
		if prefix.Len() == maxCopilotToolNameLength-18 {
			break
		}
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			prefix.WriteByte(byte(char))
		} else {
			prefix.WriteByte('_')
		}
	}
	return prefix.String() + "__" + digest[:16]
}

func nativeCopilotToolName(name, namespace string) string {
	if namespace == "" {
		return name
	}
	return stableCopilotNativeToolName(namespace, name)
}

func decodeCopilotNativeJSON(raw []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func setCopilotNativeJSON(object *jsonobject.Object, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return object.Set(key, encoded)
}

func copilotNativeToolChoice(value any, omitHosted bool) (any, error) {
	choice, ok := value.(map[string]any)
	if !ok {
		return value, nil
	}
	switch kind, _ := choice["type"].(string); kind {
	case "function", "custom":
		name, _ := choice["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("function tool_choice requires name")
		}
		namespace, _ := choice["namespace"].(string)
		return map[string]any{"type": "function", "name": nativeCopilotToolName(name, namespace)}, nil
	case "allowed_tools":
		tools, ok := choice["tools"].([]any)
		if !ok {
			return nil, fmt.Errorf("allowed_tools tool_choice requires tools")
		}
		allowed := make([]any, 0, len(tools))
		for _, tool := range tools {
			normalized, err := copilotNativeToolChoice(tool, omitHosted)
			if err != nil {
				return nil, err
			}
			if normalized != nil {
				allowed = append(allowed, normalized)
			}
		}
		if len(allowed) == 0 {
			if choice["mode"] == "required" {
				return nil, fmt.Errorf("required allowed_tools has no supported tools")
			}
			// An empty allowlist means that no remaining function may be called.
			return "none", nil
		}
		choice["tools"] = allowed
		return choice, nil
	case "tool_search":
		return nil, nil
	default:
		if omitHosted && isCopilotOmittableTool(kind) {
			return nil, nil
		}
		return choice, nil
	}
}

// Restore the client-facing tool protocol before the shared HTTP/WS proxies
// inspect events, so reasoning, session IDs, usage and failure handling stay native.
func restoreCopilotNativeResponses(resp *http.Response, mappings map[string]copilotToolInfo) {
	if len(mappings) == 0 || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	upstream := resp.Body
	contentType := resp.Header.Get("Content-Type")
	reader, writer := io.Pipe()
	resp.Body = &copilotNativeBody{ReadCloser: reader, upstream: upstream}
	resp.ContentLength = -1
	resp.Header.Del("Content-Length")
	go func() {
		defer upstream.Close()
		state := &copilotNativeToolStream{mappings: mappings, calls: make(map[string]*copilotNativeCall)}
		var err error
		if strings.Contains(contentType, "application/json") {
			var value map[string]any
			decoder := json.NewDecoder(io.LimitReader(upstream, 32<<20))
			decoder.UseNumber()
			err = decoder.Decode(&value)
			if err == nil {
				err = state.restoreOutput(value)
				if err == nil {
					err = json.NewEncoder(writer).Encode(value)
				}
			}
		} else {
			err = state.writeStream(writer, upstream)
		}
		_ = writer.CloseWithError(err)
	}()
}

type copilotNativeBody struct {
	io.ReadCloser
	upstream io.ReadCloser
}

func (b *copilotNativeBody) Close() error {
	_ = b.ReadCloser.Close()
	return b.upstream.Close()
}

type copilotNativeCall struct {
	info      copilotToolInfo
	arguments strings.Builder
	done      bool
}

type copilotNativeToolStream struct {
	mappings map[string]copilotToolInfo
	calls    map[string]*copilotNativeCall
	sequence int64
}

func (s *copilotNativeToolStream) restoreItem(item map[string]any) error {
	if item["type"] != "function_call" {
		return nil
	}
	name, _ := item["name"].(string)
	info, ok := s.mappings[name]
	if !ok {
		return nil
	}
	id, _ := item["id"].(string)
	if s.calls[id] == nil {
		s.calls[id] = &copilotNativeCall{info: info}
	}
	item["name"] = info.Name
	if info.Namespace != "" {
		item["namespace"] = info.Namespace
	}
	if info.Custom {
		arguments, _ := item["arguments"].(string)
		input := customToolInput(arguments)
		if arguments != "" && item["status"] != "in_progress" && item["status"] != "incomplete" {
			if err := validateCopilotCustomInput(info, input); err != nil {
				return err
			}
		}
		item["type"], item["input"] = "custom_tool_call", input
		delete(item, "arguments")
	}
	return nil
}

func (s *copilotNativeToolStream) restoreOutput(response map[string]any) error {
	output, _ := response["output"].([]any)
	for _, entry := range output {
		if item, ok := entry.(map[string]any); ok {
			if err := s.restoreItem(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *copilotNativeToolStream) restoreEvent(event map[string]any) ([]map[string]any, error) {
	if response, ok := event["response"].(map[string]any); ok {
		if err := s.restoreOutput(response); err != nil {
			return nil, err
		}
	}
	var prefix []map[string]any
	if item, ok := event["item"].(map[string]any); ok {
		id, _ := item["id"].(string)
		arguments, _ := item["arguments"].(string)
		if err := s.restoreItem(item); err != nil {
			return nil, err
		}
		if call := s.calls[id]; call != nil && call.info.Custom && event["type"] == "response.output_item.done" {
			var err error
			prefix, err = s.finishCustomCall(call, id, event["output_index"], arguments)
			if err != nil {
				return nil, err
			}
		}
	}
	id, _ := event["item_id"].(string)
	call := s.calls[id]
	if call != nil && call.info.Custom {
		switch event["type"] {
		case "response.function_call_arguments.delta":
			if call.done {
				return nil, nil
			}
			delta, _ := event["delta"].(string)
			call.arguments.WriteString(delta)
			return nil, nil
		case "response.function_call_arguments.done":
			arguments, ok := event["arguments"].(string)
			if !ok {
				arguments = call.arguments.String()
			}
			return s.finishCustomCall(call, id, event["output_index"], arguments)
		}
	}
	return append(prefix, event), nil
}

func (s *copilotNativeToolStream) finishCustomCall(call *copilotNativeCall, id string, index any, arguments string) ([]map[string]any, error) {
	if call.done {
		return nil, nil
	}
	call.done = true
	input := customToolInput(arguments)
	// Validate when the completed item arrives, not while it may be incomplete.
	call.arguments.Reset()
	return []map[string]any{
		{"type": "response.custom_tool_call_input.delta", "item_id": id, "output_index": index, "delta": input},
		{"type": "response.custom_tool_call_input.done", "item_id": id, "output_index": index, "input": input},
	}, nil
}

func validateCopilotCustomInput(info copilotToolInfo, input string) error {
	if info.InputPattern != nil {
		if !info.InputPattern.MatchString(input) {
			return fmt.Errorf("Copilot custom tool %q input does not match its regex grammar", info.Name)
		}
	}
	return nil
}

func (s *copilotNativeToolStream) writeStream(w io.Writer, upstream io.Reader) error {
	scanner := bufio.NewScanner(upstream)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	var data []string
	eventBytes := 0
	flush := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		eventBytes = 0
		if raw == "[DONE]" {
			_, err := io.WriteString(w, "data: [DONE]\n\n")
			return err
		}
		var event map[string]any
		if err := decodeCopilotNativeJSON([]byte(raw), &event); err != nil {
			return err
		}
		if event == nil {
			return fmt.Errorf("Copilot response event must be an object")
		}
		transformed, err := s.restoreEvent(event)
		if err != nil {
			return err
		}
		for _, restored := range transformed {
			restored["sequence_number"] = s.sequence
			s.sequence++
			encoded, err := json.Marshal(restored)
			if err != nil {
				return err
			}
			if eventType, ok := restored["type"].(string); ok {
				if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
				return err
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			eventBytes += len(line)
			if eventBytes > 32<<20 {
				return fmt.Errorf("Copilot response event exceeds size limit")
			}
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		} else if line == "" {
			if err := flush(); err != nil {
				return err
			}
		} else if strings.HasPrefix(line, ":") {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}
