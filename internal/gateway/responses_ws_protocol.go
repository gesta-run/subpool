package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const responsesWSMaxStreamIDBytes = 256

func parseResponsesWSRequest(payload []byte) (responsesWSRequest, *gatewayError) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 || len(trimmed) > maxRequestBody {
		return responsesWSRequest{}, &gatewayError{http.StatusBadRequest, "invalid response.create message", "invalid_request_error"}
	}
	if err := validateResponsesWSObject(trimmed); err != nil {
		return responsesWSRequest{}, &gatewayError{http.StatusBadRequest, err.Error(), "invalid_request_error"}
	}
	var request responsesWSRequest
	if err := json.Unmarshal(trimmed, &request); err != nil || request.Type != "response.create" {
		return responsesWSRequest{}, &gatewayError{http.StatusBadRequest, "type must be response.create", "invalid_request_error"}
	}
	if request.StreamID != nil {
		if !validResponsesWSStreamID(*request.StreamID) {
			return responsesWSRequest{}, &gatewayError{http.StatusBadRequest, "stream_id must contain 1-256 letters, numbers, underscores, hyphens, or periods", "invalid_stream_id"}
		}
	}
	request.raw = append([]byte(nil), trimmed...)
	return request, nil
}

func validResponsesWSStreamID(streamID string) bool {
	if len(streamID) == 0 || len(streamID) > responsesWSMaxStreamIDBytes {
		return false
	}
	for index := 0; index < len(streamID); index++ {
		value := streamID[index]
		if (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') ||
			(value >= '0' && value <= '9') || value == '_' || value == '-' || value == '.' {
			continue
		}
		return false
	}
	return true
}

func validateResponsesWSObject(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return errors.New("response.create message must be a JSON object")
	}
	seen := make(map[string]struct{})
	control := map[string]struct{}{"type": {}, "stream_id": {}, "previous_response_id": {}, "model": {}}
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok {
			return errors.New("invalid response.create message")
		}
		if _, critical := control[key]; critical {
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate %s field", key)
			}
			seen[key] = struct{}{}
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return errors.New("invalid response.create message")
		}
	}
	if _, err = decoder.Token(); err != nil {
		return errors.New("invalid response.create message")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("response.create message has trailing data")
	}
	return nil
}

func (r responsesWSRequest) streamID() string {
	if r.StreamID == nil {
		return ""
	}
	return *r.StreamID
}

func parseResponsesWSEvent(payload []byte) responsesWSEvent {
	var event responsesWSEvent
	_ = json.Unmarshal(payload, &event)
	return event
}

func terminalResponsesWSEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.incomplete", "response.failed", "response.done", "error":
		return true
	default:
		return false
	}
}

func responseWSRetryError(reason retryReason) (string, string) {
	switch reason {
	case retryAuth:
		return "provider_authentication_error", "provider authentication failed"
	case retryRateLimit:
		return "subpool_rate_limited", "provider is rate limited"
	case retryInvalid:
		return "invalid_request_error", "client_metadata must be an object"
	default:
		return "provider_error", "provider is unavailable"
	}
}

func bridgeRetrySafe(reason retryReason) bool {
	switch reason {
	case retryUnavailable, retryAuth, retryRefresh, retryRateLimit, retryProvider5xx:
		return true
	default:
		return false
	}
}

func readResponsesWSHTTPError(response *http.Response) (string, string) {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var value struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &value) == nil && value.Error.Message != "" {
		code := value.Error.Code
		if code == "" {
			code = value.Error.Type
		}
		if code == "" {
			code = "provider_error"
		}
		return code, value.Error.Message
	}
	return "provider_error", fmt.Sprintf("provider returned HTTP %d", response.StatusCode)
}

func (s *responsesWSSession) forwardBridgeResponse(turn *responsesWSTurn, response *http.Response) error {
	if strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		body, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBody+1))
		if err != nil || len(body) > maxRequestBody {
			return errors.New("invalid provider response")
		}
		var value map[string]any
		if json.Unmarshal(body, &value) != nil {
			return errors.New("invalid provider response")
		}
		status, _ := value["status"].(string)
		if status != "completed" && status != "incomplete" && status != "failed" {
			return errors.New("provider response is not terminal")
		}
		event, _ := json.Marshal(map[string]any{"type": "response." + status, "response": value})
		return s.forwardBridgeEvent(turn, event)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), maxRequestBody)
	for scanner.Scan() {
		data := sseData(scanner.Bytes())
		if len(data) > 0 && string(data) != "[DONE]" {
			if err := s.forwardBridgeEvent(turn, data); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if s.turnTerminal(turn) == "" {
		return errors.New("provider stream has no terminal event")
	}
	return nil
}

func (s *responsesWSSession) forwardBridgeEvent(turn *responsesWSTurn, payload []byte) error {
	var value map[string]any
	if json.Unmarshal(payload, &value) != nil || value["type"] == nil {
		return errors.New("invalid provider event")
	}
	if turn.streamID != "" {
		value["stream_id"] = turn.streamID
		payload, _ = json.Marshal(value)
	}
	event := parseResponsesWSEvent(payload)
	s.observeTurn(turn, payload, event.Type)
	return s.writeClient(payload)
}

func (s *responsesWSSession) observeTurn(turn *responsesWSTurn, payload []byte, eventType string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turn == nil || turn.finished {
		return
	}
	if responseID := responseIDFromEvent(payload); responseID != "" {
		turn.response = responseID
	}
	input, output := usageFromEvent(payload)
	if input > turn.input {
		turn.input = input
	}
	if output > turn.output {
		turn.output = output
	}
	var value map[string]any
	if json.Unmarshal(payload, &value) == nil {
		if response, ok := value["response"].(map[string]any); ok {
			if model, ok := response["model"].(string); ok && model != "" {
				turn.model = model
			}
		}
	}
	if terminalResponsesWSEvent(eventType) {
		turn.terminal = eventType
		if (eventType == "response.completed" || eventType == "response.incomplete") && turn.response != "" {
			s.rememberResponseLocked(turn.response)
		}
	}
}

func (s *responsesWSSession) rememberResponseLocked(responseID string) {
	if len(s.responses) >= responsesWSMaxRememberedIDs {
		for remembered := range s.responses {
			delete(s.responses, remembered)
			break
		}
	}
	s.responses[responseID] = struct{}{}
}

func (s *responsesWSSession) turnTerminal(turn *responsesWSTurn) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if turn == nil {
		return ""
	}
	return turn.terminal
}

func (s *responsesWSSession) currentTurn(streamID string) *responsesWSTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	stream := s.streams[streamID]
	if stream == nil || len(stream.turns) == 0 {
		return nil
	}
	return stream.turns[0]
}
