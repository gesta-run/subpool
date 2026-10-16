package copilotcompact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// InbandResponse uses the ordinary Responses item lifecycle. The only output
// is one compaction item; the final encrypted content is present in .done.
func InbandResponse(model string, item json.RawMessage, summary Summary) *http.Response {
	var body bytes.Buffer
	events := newInbandEvents(&body, model, item)
	_ = events.begin()
	_ = events.complete(item, summary)
	response := inbandHTTPResponse(io.NopCloser(bytes.NewReader(body.Bytes())))
	response.ContentLength = int64(body.Len())
	return response
}

type inbandEvents struct {
	writer   io.Writer
	response map[string]any
	initial  map[string]string
	sequence int
}

func newInbandEvents(writer io.Writer, model string, item json.RawMessage) *inbandEvents {
	id := "resp_subpool_cmp_" + itemString(item, "id")
	response := map[string]any{
		"id": id, "object": "response", "created_at": time.Now().Unix(), "model": model,
		"status": "in_progress", "output": []any{}, "error": nil, "incomplete_details": nil,
		"usage": nil, "store": false,
	}
	initial := map[string]string{"type": "compaction", "id": itemString(item, "id"), "encrypted_content": ""}
	if creator := itemString(item, "created_by"); creator != "" {
		initial["created_by"] = creator
	}
	return &inbandEvents{writer: writer, response: response, initial: initial}
}

func (events *inbandEvents) write(kind string, fields map[string]any) error {
	fields["type"], fields["sequence_number"] = kind, events.sequence
	events.sequence++
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(events.writer, "event: %s\ndata: %s\n\n", kind, raw)
	return err
}

func (events *inbandEvents) begin() error {
	for _, kind := range []string{"response.created", "response.in_progress"} {
		if err := events.write(kind, map[string]any{"response": events.response}); err != nil {
			return err
		}
	}
	return events.write("response.output_item.added", map[string]any{"output_index": 0, "item": events.initial})
}

func (events *inbandEvents) progress(sampled bool) error {
	if sampled {
		return events.write("response.compaction.compacting", map[string]any{"output_index": 0, "item_id": events.initial["id"]})
	}
	// Keep an active response observable without claiming new summary sampling.
	return events.write("response.in_progress", map[string]any{"response": events.response})
}

func (events *inbandEvents) complete(item json.RawMessage, summary Summary) error {
	if err := events.write("response.output_item.done", map[string]any{"output_index": 0, "item": item}); err != nil {
		return err
	}
	events.response["status"], events.response["output"] = "completed", []json.RawMessage{item}
	events.response["usage"] = map[string]int64{"input_tokens": summary.InputTokens, "output_tokens": summary.OutputTokens, "total_tokens": summary.InputTokens + summary.OutputTokens}
	return events.write("response.completed", map[string]any{"response": events.response})
}

func (events *inbandEvents) fail(code, message string) error {
	events.response["status"] = "failed"
	events.response["error"] = map[string]string{"type": "server_error", "code": code, "message": message}
	return events.write("response.failed", map[string]any{"response": events.response})
}

func inbandHTTPResponse(body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{
		"Content-Type": {"text/event-stream"}, "X-Subpool-Compaction": {"subpool-opaque-v1"},
	}, Body: body, ContentLength: -1}
}
