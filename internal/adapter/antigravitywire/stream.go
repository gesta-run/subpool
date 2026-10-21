package antigravitywire

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type streamState struct {
	scope, model, id, finish string
	signatures               *Signatures
	calls                    []any
	text, reasoning          strings.Builder
	usage                    map[string]any
	input, output            int64
	emit                     func(map[string]any) error
	size                     int
	nativePrefix             []any
}

// Response converts the upstream SSE once; the shared renderer owns client wire formats.
func Response(upstream *http.Response, stream bool, scope, model string, signatures *Signatures) (*http.Response, error) {
	state := &streamState{scope: scope, model: model, id: "chatcmpl-ag-" + newID(), signatures: signatures}
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1}
	if !stream {
		defer upstream.Body.Close()
		if err := state.read(upstream.Body); err != nil {
			return nil, err
		}
		message := map[string]any{"role": "assistant", "content": state.text.String()}
		if len(state.calls) > 0 {
			message["tool_calls"] = state.calls
		}
		if state.reasoning.Len() > 0 {
			message["reasoning_content"] = state.reasoning.String()
		}
		body, err := json.Marshal(map[string]any{"id": state.id, "object": "chat.completion", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": state.finish}}, "usage": state.usage})
		if err != nil {
			return nil, err
		}
		response.Header.Set("Content-Type", "application/json")
		response.Body = io.NopCloser(strings.NewReader(string(body)))
		return response, nil
	}
	reader, writer := io.Pipe()
	response.Header.Set("Content-Type", "text/event-stream")
	response.Body = &streamBody{PipeReader: reader, upstream: upstream.Body}
	state.emit = func(delta map[string]any) error {
		return state.chunk(writer, []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}, nil)
	}
	go func() {
		defer upstream.Body.Close()
		err := state.read(upstream.Body)
		if err != nil {
			_, _ = io.WriteString(writer, "data: {\"error\":{\"type\":\"provider_error\",\"message\":\"Antigravity stream failed or ended without a terminal response\"}}\n\n")
		} else {
			err = state.chunk(writer, []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": state.finish}}, nil)
			if err == nil {
				err = state.chunk(writer, []any{}, state.usage)
			}
			if err == nil {
				_, err = io.WriteString(writer, "data: [DONE]\n\n")
			}
		}
		_ = writer.CloseWithError(err)
	}()
	return response, nil
}

type streamBody struct {
	*io.PipeReader
	upstream io.Closer
}

func (b *streamBody) Close() error { _ = b.PipeReader.Close(); return b.upstream.Close() }

func newID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(id[:])
}

func (s *streamState) chunk(writer io.Writer, choices []any, usage any) error {
	payload := map[string]any{"id": s.id, "object": "chat.completion.chunk", "model": s.model, "created": time.Now().Unix(), "choices": choices}
	if usage != nil {
		payload["usage"] = usage
	}
	raw, err := json.Marshal(payload)
	if err == nil {
		_, err = fmt.Fprintf(writer, "data: %s\n\n", raw)
	}
	return err
}

func (s *streamState) read(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	var event strings.Builder
	consume := func() error {
		if event.Len() == 0 {
			return nil
		}
		raw := event.String()
		event.Reset()
		return s.consume([]byte(raw))
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if event.Len() > 0 {
				event.WriteByte('\n')
			}
			event.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			if event.Len() > 8<<20 {
				return errors.New("Antigravity event exceeds size limit")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if err := consume(); err != nil {
		return err
	}
	if s.finish == "" {
		return errors.New("Antigravity stream ended without a terminal response")
	}
	return nil
}

func (s *streamState) consume(raw []byte) error {
	if string(raw) == "[DONE]" {
		return nil
	}
	var payload struct {
		Response json.RawMessage `json:"response"`
		Error    json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return errors.New("invalid Antigravity event")
	}
	if len(payload.Error) > 0 && string(payload.Error) != "null" {
		return errors.New("Antigravity stream returned an error")
	}
	if len(payload.Response) > 0 {
		raw = payload.Response
	}
	var event struct {
		Candidates []struct {
			Content struct {
				Parts []map[string]any `json:"parts"`
			} `json:"content"`
			Finish string `json:"finishReason"`
		} `json:"candidates"`
		Usage *struct {
			Prompt     int64 `json:"promptTokenCount"`
			Candidates int64 `json:"candidatesTokenCount"`
			Thoughts   int64 `json:"thoughtsTokenCount"`
			Cached     int64 `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&event) != nil {
		return errors.New("invalid Antigravity response")
	}
	if len(event.Candidates) > 1 {
		return errors.New("unexpected multiple Antigravity candidates")
	}
	if event.Usage != nil {
		u := event.Usage
		s.input, s.output = max(0, u.Prompt), max(0, u.Candidates)+max(0, u.Thoughts)
		s.usage = map[string]any{"prompt_tokens": s.input, "completion_tokens": s.output, "total_tokens": s.input + s.output,
			"prompt_tokens_details": map[string]any{"cached_tokens": min(s.input, max(0, u.Cached))}, "completion_tokens_details": map[string]any{"reasoning_tokens": max(0, u.Thoughts)}}
	}
	if event.PromptFeedback.BlockReason != "" {
		s.finish = "content_filter"
	}
	for _, candidate := range event.Candidates {
		for _, part := range candidate.Content.Parts {
			if err := s.part(part); err != nil {
				return err
			}
		}
		if candidate.Finish != "" {
			switch candidate.Finish {
			case "STOP":
				s.finish = "stop"
				if len(s.calls) > 0 {
					s.finish = "tool_calls"
				}
			case "MAX_TOKENS":
				s.finish = "length"
			case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
				s.finish = "content_filter"
			default:
				return errors.New("Antigravity generation ended unsuccessfully")
			}
		}
	}
	return nil
}

func (s *streamState) part(part map[string]any) error {
	// Keep the native prefix, including signed empty text and visible text, in
	// order. Chat/Responses history flattens these parts across message items.
	if part["functionCall"] == nil && strings.HasPrefix(strings.ToLower(s.model), "claude-") {
		s.nativePrefix = append(s.nativePrefix, part)
	}
	if text, ok := part["text"].(string); ok {
		s.size += len(text)
		if s.size > 32<<20 {
			return errors.New("Antigravity output exceeds size limit")
		}
		field := "content"
		if part["thought"] == true {
			field = "reasoning_content"
			if s.emit == nil {
				s.reasoning.WriteString(text)
			}
		} else if s.emit == nil {
			s.text.WriteString(text)
		}
		if s.emit != nil && text != "" {
			if err := s.emit(map[string]any{field: text}); err != nil {
				return err
			}
		}
	}
	if function, ok := part["functionCall"].(map[string]any); ok {
		name, _ := function["name"].(string)
		if name == "" || len(s.calls) >= 128 {
			return errors.New("invalid Antigravity function call")
		}
		args, ok := function["args"].(map[string]any)
		if !ok {
			return errors.New("Antigravity function arguments must be an object")
		}
		raw, _ := json.Marshal(args)
		s.size += len(raw)
		if s.size > 32<<20 {
			return errors.New("Antigravity output exceeds size limit")
		}
		id, _ := function["id"].(string)
		if id == "" {
			id = "call_ag_" + newID()
		}
		sig, _ := part["thoughtSignature"].(string)
		if err := s.signatures.Save(s.scope, s.model, id, name, args, sig, s.nativePrefix); err != nil {
			return errors.New("could not preserve Antigravity tool signature")
		}
		s.nativePrefix = nil
		call := map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}
		s.calls = append(s.calls, call)
		if s.emit != nil {
			return s.emit(map[string]any{"tool_calls": []any{map[string]any{"index": len(s.calls) - 1, "id": id, "type": "function", "function": call["function"]}}})
		}
	}
	if part["inlineData"] != nil || part["executableCode"] != nil || part["codeExecutionResult"] != nil {
		return errors.New("Antigravity returned an unsupported output modality")
	}
	return nil
}
