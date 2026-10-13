package responsesws

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/coder/websocket"
)

func (s *responsesWSSession) readClient() {
	first := true
	for {
		readCtx, cancel := context.WithCancel(s.ctx)
		var firstMessageTimer *time.Timer
		if first {
			firstMessageTimer = time.AfterFunc(responsesWSFirstMessageTimeout, cancel)
		}
		messageType, reader, err := s.conn.Reader(readCtx)
		if firstMessageTimer != nil {
			firstMessageTimer.Stop()
		}
		if err != nil {
			cancel()
			return
		}
		if messageType != websocket.MessageText {
			cancel()
			s.close(websocket.StatusUnsupportedData, "text messages required")
			return
		}
		bodyState, err := s.hub.backend.RequestBodyState(readCtx)
		if err != nil {
			cancel()
			slog.Error("WebSocket request body settings unavailable", "error", err)
			s.close(websocket.StatusTryAgainLater, "request body settings are temporarily unavailable")
			return
		}
		bodyTimer := time.AfterFunc(bodyState.ReadTimeout, cancel)
		payload, releaseBody, err := s.hub.backend.ReadRequestBody(reader, bodyState)
		bodyTimer.Stop()
		cancel()
		if err != nil {
			reason := "read_failed"
			closeStatus := websocket.StatusInternalError
			closeReason := "request body could not be read"
			if errors.Is(err, ErrRequestBodyCapacity) {
				reason = "capacity"
				closeStatus = websocket.StatusTryAgainLater
				closeReason = "request body capacity is temporarily unavailable"
			} else if errors.Is(err, ErrRequestBodyTooLarge) || errors.Is(err, websocket.ErrMessageTooBig) {
				reason = "too_large"
				closeStatus = websocket.StatusMessageTooBig
				closeReason = "request body is too large"
			}
			slog.Warn("WebSocket request body rejected", "reason", reason, "error", err, "max_bytes", bodyState.MaxBytes,
				"inflight_buffer_bytes", bodyState.InflightBytes, "max_inflight_buffer_bytes", bodyState.MaxInflightBytes)
			s.close(closeStatus, closeReason)
			return
		}
		first = false
		s.touch()
		request, requestErr := parseResponsesWSRequest(payload)
		if requestErr != nil {
			releaseBody()
			s.sendError("", requestErr.Code, requestErr.Message)
			continue
		}
		if !s.handleRequest(request, releaseBody) {
			return
		}
	}
}
