package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gesta-run/subpool/internal/adapter"
	"github.com/gesta-run/subpool/internal/protocol/responseevent"
)

func (s *Server) proxyUpstreamError(w http.ResponseWriter, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func (s *Server) proxyImageJSON(w http.ResponseWriter, keyID, accountID, model string, resp *http.Response) {
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
		s.addAccountUsage(keyID, accountID, "", model, envelope.Usage.InputTokens, envelope.Usage.OutputTokens)
	}
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

func (s *Server) responseObserver(keyID, poolID, accountID string) adapter.Observer {
	return adapter.Observer{
		Now:          s.now,
		NextSequence: func() uint64 { return s.eventSeq.Add(1) },
		Usage: func(responseID, model string, input, output int64) {
			s.addAccountUsage(keyID, accountID, responseID, model, input, output)
		},
		Session: func(responseID string) {
			if poolID != "" && accountID != "" {
				s.saveSession(keyID, poolID, responseID, accountID)
			}
		},
		Limit: func(limit responseevent.LimitKind) { s.recordProviderStreamLimit(accountID, limit) },
	}
}

// Usage-only endpoints need attribution without creating a conversation binding.
func (s *Server) addAccountUsage(keyID, accountID, responseID, model string, input, output int64) {
	fallback := s.randomUsageEventHash()
	s.addUsage(keyID, s.usageEventHash(responseID, fallback), model, input, output)
	if accountID == "" {
		return
	}
	hash := fallback
	if responseID != "" {
		hash = responseevent.SessionHash(responseID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = s.store.RecordProviderRequest(ctx, keyID, hash, accountID, s.now())
		if err == nil {
			return
		}
		if attempt < 3 && !waitRetry(ctx, time.Duration(attempt)*25*time.Millisecond) {
			break
		}
	}
	slog.Error("provider request recording failed", "api_key_id", keyID, "error", err)
}
