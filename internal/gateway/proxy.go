package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/gateway/clientquota"
	"github.com/gesta-run/subpool/internal/gateway/responseevent"
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

func (s *Server) proxyUpstreamError(w http.ResponseWriter, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func (s *Server) proxyImageJSON(w http.ResponseWriter, keyID, model string, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(w, resp.Body)
		return
	}
	var envelope struct {
		Usage struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(io.TeeReader(resp.Body, w))
	decodeErr := decoder.Decode(&envelope)
	_, copyErr := io.Copy(w, resp.Body)
	if decodeErr == nil && copyErr == nil && (envelope.Usage.InputTokens > 0 || envelope.Usage.OutputTokens > 0) {
		s.addUsage(keyID, s.randomUsageEventHash(), model, envelope.Usage.InputTokens, envelope.Usage.OutputTokens)
	}
}

func (s *Server) proxyResponsesStream(w http.ResponseWriter, r *http.Request, keyID, poolID, accountID, model string, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(resp.StatusCode)
	responseID := ""
	fallbackEventHash := s.randomUsageEventHash()
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
	s.recordProviderStreamLimit(accountID, limit)
	if err != nil || !terminal {
		return
	}
	if input > 0 || output > 0 {
		s.addUsage(keyID, s.usageEventHash(responseID, fallbackEventHash), model, input, output)
	}
	if responseID != "" && accountID != "" {
		s.saveSession(keyID, poolID, responseID, accountID)
	}
}

func (s *Server) proxyResponsesJSON(w http.ResponseWriter, r *http.Request, keyID, poolID, accountID, model string, resp *http.Response) {
	fallbackEventHash := s.randomUsageEventHash()
	value, input, output, _, err := completedResponse(resp)
	if err != nil {
		var failure *responseEventError
		if errors.As(err, &failure) {
			s.recordProviderStreamLimit(accountID, failure.limit)
			writeResponseEventError(w, failure)
			return
		}
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	responseID := ""
	if response, ok := value.(map[string]any); ok {
		responseID, _ = response["id"].(string)
		if responseID != "" && accountID != "" {
			s.saveSession(keyID, poolID, responseID, accountID)
		}
	}
	if input > 0 || output > 0 {
		s.addUsage(keyID, s.usageEventHash(responseID, fallbackEventHash), model, input, output)
	}
	clientquota.CopyHeaders(w.Header(), resp.Header)
	writeJSON(w, http.StatusOK, value)
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

func (s *Server) addUsage(keyID string, eventHash []byte, model string, input, output int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = s.store.AddUsage(ctx, keyID, eventHash, model, s.now(), input, output)
		if err == nil {
			return
		}
		if attempt < 3 && !waitRetry(ctx, time.Duration(attempt)*25*time.Millisecond) {
			break
		}
	}
	slog.Error("usage aggregation failed", "api_key_id", keyID, "attempts", 3, "error", err)
}

func (s *Server) usageEventHash(responseID string, fallback []byte) []byte {
	if responseID == "" {
		return fallback
	}
	return s.keys.Digest("usage-event:" + responseID)
}

func (s *Server) randomUsageEventHash() []byte {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err == nil {
		return s.keys.Digest("usage-event-fallback:" + string(random))
	}
	sequence := s.eventSeq.Add(1)
	return s.keys.Digest(fmt.Sprintf("usage-event-fallback:%d:%d", s.now().UnixNano(), sequence))
}
func (s *Server) saveSession(keyID, poolID, responseID, accountID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = s.store.SaveSessionBinding(ctx, keyID, poolID, responseevent.SessionHash(responseID), accountID, s.now().Add(24*time.Hour))
		if err == nil {
			return
		}
		if attempt < 3 && !waitRetry(ctx, time.Duration(attempt)*25*time.Millisecond) {
			break
		}
	}
	slog.Error("session binding failed", "api_key_id", keyID, "attempts", 3, "error", err)
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
