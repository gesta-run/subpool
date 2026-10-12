package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/gesta-run/subpool/internal/protocol/responseevent"
	"io"
	"net/http"
	"strings"
)

type copilotResponsesStreamState struct {
	responseID   string
	model        string
	created      int64
	sequence     int64
	started      bool
	failed       bool
	finishReason string
	nextOutput   int
	message      *copilotStreamMessage
	tools        map[int]*copilotStreamTool
	inputTokens  int64
	outputTokens int64
	usage        map[string]any
	copilotTools map[string]ToolInfo
	limit        responseevent.LimitKind
}

type copilotStreamMessage struct {
	id           string
	outputIndex  int
	text         strings.Builder
	refusal      strings.Builder
	textIndex    int
	refusalIndex int
	nextContent  int
}

type copilotStreamTool struct {
	id           string
	name         strings.Builder
	responseName string
	namespace    string
	arguments    strings.Builder
	outputIndex  int
	added        bool
	custom       bool
}

func (s *Renderer) proxyCopilotResponsesStream(w http.ResponseWriter, model string, copilotTools map[string]ToolInfo, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	state, err := s.writeCopilotResponsesStream(w, model, copilotTools, resp.Body)
	s.hooks.Limit(state.limit)
	if err != nil || state.failed {
		return
	}
	if state.inputTokens > 0 || state.outputTokens > 0 {
		s.hooks.Usage(state.responseID, state.model, state.inputTokens, state.outputTokens)
	}
	if state.responseID != "" {
		s.hooks.Session(state.responseID)
	}
}

func (s *Renderer) writeCopilotResponsesStream(w http.ResponseWriter, model string, copilotTools map[string]ToolInfo, reader io.Reader) (*copilotResponsesStreamState, error) {
	fallbackID := fmt.Sprintf("subpool-%d-%d", s.hooks.Now().UnixNano(), s.hooks.NextSequence())
	state := &copilotResponsesStreamState{
		responseID: copilotResponseID("", fallbackID), model: model, created: s.hooks.Now().Unix(),
		tools: make(map[int]*copilotStreamTool), copilotTools: copilotTools,
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	for scanner.Scan() {
		data := responseevent.SSEData(scanner.Bytes())
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal(data, &chunk) == nil {
			if state.limit == responseevent.LimitNone {
				state.limit = responseevent.ClassifyLimit(data)
			}
			state.consume(w, chunk)
			if state.failed {
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return state, err
	}
	if state.failed {
		return state, nil
	}
	if !state.finish(w) {
		return state, fmt.Errorf("Copilot stream did not contain a terminal response")
	}
	return state, nil
}

func (s *copilotResponsesStreamState) consume(w http.ResponseWriter, chunk map[string]any) {
	if upstreamError, exists := chunk["error"]; exists && upstreamError != nil {
		s.writeError(w, upstreamError)
		return
	}
	if !s.started {
		if chatID, _ := chunk["id"].(string); chatID != "" {
			s.responseID = copilotResponseID(chatID, strings.TrimPrefix(s.responseID, "resp_"))
		}
		if model, _ := chunk["model"].(string); model != "" {
			s.model = model
		}
		if created := responseevent.Number(chunk["created"]); created > 0 {
			s.created = created
		}
	}
	if usage, input, output := chatUsageToResponses(chunk["usage"]); input > 0 || output > 0 {
		s.usage, s.inputTokens, s.outputTokens = usage, input, output
	}
	choices, _ := chunk["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	choice, _ := choices[0].(map[string]any)
	delta, _ := choice["delta"].(map[string]any)
	s.start(w)
	if content, ok := delta["content"].(string); ok && content != "" {
		s.writeTextDelta(w, content)
	}
	if refusal, ok := delta["refusal"].(string); ok && refusal != "" {
		s.writeRefusalDelta(w, refusal)
	}
	if calls, ok := delta["tool_calls"].([]any); ok {
		s.writeToolDeltas(w, calls)
	}
	if finish, ok := choice["finish_reason"].(string); ok && finish != "" {
		s.finishReason = finish
	}
}

func (s *copilotResponsesStreamState) writeError(w http.ResponseWriter, value any) {
	code := "provider_error"
	message := "GitHub Copilot stream failed"
	var param any
	if upstream, ok := value.(map[string]any); ok {
		if upstreamCode, _ := upstream["code"].(string); upstreamCode != "" {
			code = upstreamCode
		} else if upstreamType, _ := upstream["type"].(string); upstreamType != "" {
			code = upstreamType
		}
		if upstreamMessage, _ := upstream["message"].(string); upstreamMessage != "" {
			message = upstreamMessage
		}
		param = upstream["param"]
	} else if upstreamMessage, ok := value.(string); ok && upstreamMessage != "" {
		message = upstreamMessage
	}
	s.failed = true
	s.emit(w, map[string]any{"type": "error", "code": code, "message": message, "param": param})
}

func (s *copilotResponsesStreamState) start(w http.ResponseWriter) {
	if s.started {
		return
	}
	s.started = true
	response := newCopilotResponse(s.responseID, s.model, s.created, "in_progress", []any{}, nil)
	s.emit(w, map[string]any{"type": "response.created", "response": response})
	s.emit(w, map[string]any{"type": "response.in_progress", "response": response})
}

func (s *copilotResponsesStreamState) writeTextDelta(w http.ResponseWriter, delta string) {
	message := s.ensureMessage(w)
	if message.textIndex < 0 {
		message.textIndex = message.nextContent
		message.nextContent++
		part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}, "logprobs": []any{}}
		s.emit(w, map[string]any{"type": "response.content_part.added", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.textIndex, "part": part})
	}
	message.text.WriteString(delta)
	s.emit(w, map[string]any{
		"type": "response.output_text.delta", "item_id": message.id, "output_index": message.outputIndex,
		"content_index": message.textIndex, "delta": delta, "logprobs": []any{},
	})
}

func (s *copilotResponsesStreamState) writeRefusalDelta(w http.ResponseWriter, delta string) {
	message := s.ensureMessage(w)
	if message.refusalIndex < 0 {
		message.refusalIndex = message.nextContent
		message.nextContent++
		part := map[string]any{"type": "refusal", "refusal": ""}
		s.emit(w, map[string]any{"type": "response.content_part.added", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.refusalIndex, "part": part})
	}
	message.refusal.WriteString(delta)
	s.emit(w, map[string]any{
		"type": "response.refusal.delta", "item_id": message.id, "output_index": message.outputIndex,
		"content_index": message.refusalIndex, "delta": delta,
	})
}

func (s *copilotResponsesStreamState) ensureMessage(w http.ResponseWriter) *copilotStreamMessage {
	if s.message != nil {
		return s.message
	}
	s.message = &copilotStreamMessage{
		id: copilotMessageID(s.responseID), outputIndex: s.nextOutput, textIndex: -1, refusalIndex: -1,
	}
	s.nextOutput++
	item := map[string]any{"id": s.message.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
	s.emit(w, map[string]any{"type": "response.output_item.added", "output_index": s.message.outputIndex, "item": item})
	return s.message
}

func (s *copilotResponsesStreamState) writeToolDeltas(w http.ResponseWriter, calls []any) {
	for _, rawCall := range calls {
		call, ok := rawCall.(map[string]any)
		if !ok {
			continue
		}
		index := int(responseevent.Number(call["index"]))
		tool := s.tools[index]
		if tool == nil {
			tool = &copilotStreamTool{id: fmt.Sprintf("call_%d", index), outputIndex: s.nextOutput}
			s.tools[index] = tool
			s.nextOutput++
		}
		if id, _ := call["id"].(string); id != "" {
			tool.id = id
		}
		function, _ := call["function"].(map[string]any)
		if name, _ := function["name"].(string); name != "" {
			tool.name.WriteString(name)
		}
		if arguments, _ := function["arguments"].(string); arguments != "" {
			s.addTool(w, tool)
			tool.arguments.WriteString(arguments)
			if !tool.custom {
				s.emit(w, map[string]any{
					"type": "response.function_call_arguments.delta", "item_id": tool.id,
					"output_index": tool.outputIndex, "delta": arguments,
				})
			}
		}
	}
}

func (s *copilotResponsesStreamState) addTool(w http.ResponseWriter, tool *copilotStreamTool) {
	if tool.added {
		return
	}
	tool.added = true
	upstreamName := tool.name.String()
	info, mapped := s.copilotTools[upstreamName]
	if mapped {
		tool.responseName = info.Name
		tool.namespace = info.Namespace
		tool.custom = info.Custom
	} else {
		tool.responseName = upstreamName
	}
	item := responseFunctionCall(tool.id, tool.responseName, "", "in_progress", tool.namespace)
	if tool.custom {
		item = responseCustomToolCall(tool.id, tool.responseName, "", "in_progress", tool.namespace)
	}
	s.emit(w, map[string]any{"type": "response.output_item.added", "output_index": tool.outputIndex, "item": item})
}

func (s *copilotResponsesStreamState) finish(w http.ResponseWriter) bool {
	if !s.started || s.finishReason == "" {
		return false
	}
	output := make([]any, s.nextOutput)
	if s.message != nil {
		output[s.message.outputIndex] = s.finishMessage(w)
	}
	for outputIndex := 0; outputIndex < s.nextOutput; outputIndex++ {
		for _, tool := range s.tools {
			if tool.outputIndex != outputIndex {
				continue
			}
			s.addTool(w, tool)
			arguments := tool.arguments.String()
			item := responseFunctionCall(tool.id, tool.responseName, arguments, "completed", tool.namespace)
			if tool.custom {
				input := CustomToolInput(arguments)
				s.emit(w, map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": tool.id, "output_index": tool.outputIndex, "delta": input})
				s.emit(w, map[string]any{"type": "response.custom_tool_call_input.done", "item_id": tool.id, "output_index": tool.outputIndex, "input": input})
				item = responseCustomToolCall(tool.id, tool.responseName, input, "completed", tool.namespace)
			} else {
				s.emit(w, map[string]any{"type": "response.function_call_arguments.done", "item_id": tool.id, "output_index": tool.outputIndex, "arguments": arguments})
			}
			s.emit(w, map[string]any{"type": "response.output_item.done", "output_index": tool.outputIndex, "item": item})
			output[tool.outputIndex] = item
			break
		}
	}
	status, incomplete := responseStatus(s.finishReason)
	if s.usage == nil {
		s.usage, _, _ = chatUsageToResponses(nil)
	}
	response := newCopilotResponse(s.responseID, s.model, s.created, status, output, s.usage)
	response["incomplete_details"] = incomplete
	s.emit(w, map[string]any{"type": "response." + status, "response": response})
	return true
}

func (s *copilotResponsesStreamState) finishMessage(w http.ResponseWriter) map[string]any {
	message := s.message
	content := make([]any, message.nextContent)
	if message.textIndex >= 0 {
		text := message.text.String()
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
		s.emit(w, map[string]any{"type": "response.output_text.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.textIndex, "text": text, "logprobs": []any{}})
		s.emit(w, map[string]any{"type": "response.content_part.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.textIndex, "part": part})
		content[message.textIndex] = part
	}
	if message.refusalIndex >= 0 {
		refusal := message.refusal.String()
		part := map[string]any{"type": "refusal", "refusal": refusal}
		s.emit(w, map[string]any{"type": "response.refusal.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.refusalIndex, "refusal": refusal})
		s.emit(w, map[string]any{"type": "response.content_part.done", "item_id": message.id, "output_index": message.outputIndex, "content_index": message.refusalIndex, "part": part})
		content[message.refusalIndex] = part
	}
	item := map[string]any{"id": message.id, "type": "message", "status": "completed", "role": "assistant", "content": content}
	s.emit(w, map[string]any{"type": "response.output_item.done", "output_index": message.outputIndex, "item": item})
	return item
}

func (s *copilotResponsesStreamState) emit(w http.ResponseWriter, event map[string]any) {
	event["sequence_number"] = s.sequence
	s.sequence++
	writeSSE(w, event)
}
