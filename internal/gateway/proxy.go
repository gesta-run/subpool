package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func (s *Server) proxyUpstreamError(w http.ResponseWriter, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func (s *Server) proxyResponsesStream(w http.ResponseWriter, r *http.Request, keyID, poolID, accountID, model string, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(resp.StatusCode)
	responseID := ""
	fallbackEventHash := s.randomUsageEventHash()
	terminal := false
	input, output, err := copySSE(w, resp.Body, func(data []byte) {
		if id := responseIDFromEvent(data); id != "" {
			responseID = id
		}
		if event := eventType(data); event == "response.completed" || event == "response.incomplete" {
			terminal = true
		}
	})
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
			if data := sseData(line); len(data) > 0 {
				observe(data)
				i, o := usageFromEvent(data)
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

func sseData(line []byte) []byte {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "data:") {
		return nil
	}
	return []byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
}

func completedResponse(resp *http.Response) (any, int64, int64, string, error) {
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var value any
		err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&value)
		raw, _ := json.Marshal(value)
		i, o := usageFromEvent(raw)
		status := ""
		if response, ok := value.(map[string]any); ok {
			status, _ = response["status"].(string)
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
		data := sseData(scanner.Bytes())
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		i, o := usageFromEvent(data)
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
		eventType, _ := event["type"].(string)
		if eventType == "response.output_item.added" || eventType == "response.output_item.done" {
			if item, ok := event["item"].(map[string]any); ok {
				index := int(number(event["output_index"]))
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

func usageFromEvent(data []byte) (int64, int64) {
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
	input := number(usage["input_tokens"])
	if input == 0 {
		input = number(usage["prompt_tokens"])
	}
	output := number(usage["output_tokens"])
	if output == 0 {
		output = number(usage["completion_tokens"])
	}
	return input, output
}

func eventType(data []byte) string {
	var value struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &value)
	return value.Type
}

func responseIDFromEvent(data []byte) string {
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
		if id, ok := response["id"].(string); ok {
			return id
		}
	}
	return ""
}
func sessionHash(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
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
		err = s.store.SaveSessionBinding(ctx, keyID, poolID, sessionHash(responseID), accountID, s.now().Add(24*time.Hour))
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
func number(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
