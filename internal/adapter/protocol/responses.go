package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gesta-run/subpool/internal/protocol/responseevent"
	"io"
	"net/http"
	"strings"
)

type responseEventError struct {
	payload map[string]any
	limit   responseevent.LimitKind
}

func (e *responseEventError) Error() string {
	message, _ := e.payload["message"].(string)
	if message == "" {
		return "provider response failed"
	}
	return message
}

func (s *Renderer) proxyResponsesStream(w http.ResponseWriter, model string, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(resp.StatusCode)
	responseID := ""
	terminal := false
	limit := responseevent.LimitNone
	input, output, err := copySSE(w, resp.Body, func(data []byte) {
		if id := responseevent.ResponseID(data); id != "" {
			responseID = id
		}
		if limit == responseevent.LimitNone {
			limit = responseevent.ClassifyLimit(data)
		}
		if event := eventType(data); event == "response.completed" || event == "response.incomplete" {
			terminal = true
		}
	})
	s.hooks.Limit(limit)
	if err != nil || !terminal {
		return
	}
	if input > 0 || output > 0 {
		s.hooks.Usage(responseID, model, input, output)
	}
	if responseID != "" {
		s.hooks.Session(responseID)
	}
}

func (s *Renderer) proxyResponsesJSON(w http.ResponseWriter, model string, resp *http.Response) {
	value, input, output, _, err := completedResponse(resp)
	if err != nil {
		var failure *responseEventError
		if errors.As(err, &failure) {
			s.hooks.Limit(failure.limit)
			writeResponseEventError(w, failure)
			return
		}
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	responseID := ""
	if response, ok := value.(map[string]any); ok {
		responseID, _ = response["id"].(string)
		if responseID != "" {
			s.hooks.Session(responseID)
		}
	}
	if input > 0 || output > 0 {
		s.hooks.Usage(responseID, model, input, output)
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Renderer) proxyCompactJSON(w http.ResponseWriter, model string, resp *http.Response) {
	var response map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&response); err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider compaction response", "provider_error")
		return
	}
	if _, ok := response["output"].([]any); !ok || response["object"] != "response.compaction" {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider compaction response", "provider_error")
		return
	}
	// The entire returned window is canonical, including retained messages and
	// opaque compaction items. Never prune or translate any output item.
	raw, _ := json.Marshal(response)
	input, output := responseevent.Usage(raw)
	id, _ := response["id"].(string)
	if input > 0 || output > 0 {
		s.hooks.Usage(id, model, input, output)
	}
	copyResponseHeaders(w.Header(), resp.Header)
	writeJSON(w, resp.StatusCode, response)
}

func copySSE(w http.ResponseWriter, reader io.Reader, observe func([]byte)) (int64, int64, error) {
	buffered := bufio.NewReaderSize(reader, 32<<10)
	var input, output int64
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				return input, output, writeErr
			}
			if data := responseevent.SSEData(line); len(data) > 0 {
				observe(data)
				i, o := responseevent.Usage(data)
				if i > input {
					input = i
				}
				if o > output {
					output = o
				}
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return input, output, nil
			}
			return input, output, err
		}
	}
}

func completedResponse(resp *http.Response) (any, int64, int64, string, error) {
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var value any
		err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&value)
		raw, _ := json.Marshal(value)
		i, o := responseevent.Usage(raw)
		status := ""
		if response, ok := value.(map[string]any); ok {
			status, _ = response["status"].(string)
		}
		if failure := responseFailure(raw); failure != nil {
			return nil, i, o, status, failure
		}
		if err == nil && status != "completed" && status != "incomplete" {
			err = fmt.Errorf("provider response is not terminal")
		}
		return value, i, o, status, err
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	var completed any
	status := ""
	var input, output int64
	var streamedOutput []any
	for scanner.Scan() {
		data := responseevent.SSEData(scanner.Bytes())
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		i, o := responseevent.Usage(data)
		if i > input {
			input = i
		}
		if o > output {
			output = o
		}
		var event map[string]any
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		if failure := responseFailure(data); failure != nil {
			return nil, input, output, "failed", failure
		}
		eventType, _ := event["type"].(string)
		if eventType == "response.output_item.added" || eventType == "response.output_item.done" {
			if item, ok := event["item"].(map[string]any); ok {
				index := int(responseevent.Number(event["output_index"]))
				for len(streamedOutput) <= index {
					streamedOutput = append(streamedOutput, nil)
				}
				streamedOutput[index] = item
			}
		}
		if eventType == "response.completed" || eventType == "response.incomplete" {
			completed = event["response"]
			status = strings.TrimPrefix(eventType, "response.")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, 0, "", err
	}
	if completed == nil {
		return nil, 0, 0, "", fmt.Errorf("provider stream did not contain a terminal response")
	}
	if response, ok := completed.(map[string]any); ok && len(streamedOutput) > 0 {
		if terminalOutput, ok := response["output"].([]any); !ok || len(terminalOutput) == 0 {
			outputItems := make([]any, 0, len(streamedOutput))
			for _, item := range streamedOutput {
				if item != nil {
					outputItems = append(outputItems, item)
				}
			}
			response["output"] = outputItems
		}
	}
	return completed, input, output, status, nil
}

func responseFailure(data []byte) *responseEventError {
	var event map[string]any
	if json.Unmarshal(data, &event) != nil {
		return nil
	}
	eventType, _ := event["type"].(string)
	status, _ := event["status"].(string)
	response, _ := event["response"].(map[string]any)
	responseStatus, _ := response["status"].(string)
	_, hasRawError := event["error"]
	if eventType != "error" && eventType != "response.failed" && status != "failed" && responseStatus != "failed" && !(eventType == "" && hasRawError && event["error"] != nil) {
		return nil
	}
	payload := event["error"]
	if response != nil && response["error"] != nil {
		payload = response["error"]
	}
	return &responseEventError{payload: normalizeResponseError(payload, event), limit: responseevent.ClassifyLimit(data)}
}

func normalizeResponseError(payload any, event map[string]any) map[string]any {
	if value, ok := payload.(map[string]any); ok {
		return value
	}
	message, _ := payload.(string)
	if message == "" {
		message, _ = event["message"].(string)
	}
	if message == "" {
		message = "provider response failed"
	}
	errorType, _ := event["type"].(string)
	if errorType == "" || errorType == "error" || errorType == "response.failed" {
		errorType = "provider_error"
	}
	code, _ := event["code"].(string)
	if code == "" {
		code = errorType
	}
	return map[string]any{"message": message, "type": errorType, "code": code}
}

func writeResponseEventError(w http.ResponseWriter, failure *responseEventError) {
	status := http.StatusBadGateway
	if failure.limit != responseevent.LimitNone {
		status = http.StatusTooManyRequests
	}
	writeJSON(w, status, map[string]any{"error": failure.payload})
}

func eventType(data []byte) string {
	var value struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &value)
	return value.Type
}

type pipeResponseWriter struct {
	header http.Header
	writer io.Writer
}

func (w *pipeResponseWriter) Header() http.Header               { return w.header }
func (w *pipeResponseWriter) Write(payload []byte) (int, error) { return w.writer.Write(payload) }
func (w *pipeResponseWriter) WriteHeader(int)                   {}
func (w *pipeResponseWriter) Flush()                            {}

func (s *Renderer) BridgeResponses(resp *http.Response, model string, copilotTools map[string]ToolInfo) *http.Response {
	header := resp.Header.Clone()
	header.Set("Content-Type", "text/event-stream")
	reader, writer := io.Pipe()
	go func() {
		defer resp.Body.Close()
		sink := &pipeResponseWriter{header: make(http.Header), writer: writer}
		_, err := s.writeCopilotResponsesStream(sink, model, copilotTools, resp.Body)
		_ = writer.CloseWithError(err)
	}()
	return &http.Response{StatusCode: resp.StatusCode, Header: header, Body: &copilotNativeBody{ReadCloser: reader, upstream: resp.Body}}
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func copyResponseHeaders(dst, src http.Header) {
	for name, values := range src {
		switch strings.ToLower(name) {
		case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "content-length":
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}
