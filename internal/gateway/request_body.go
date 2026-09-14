package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultMaxRequestBodyBytes         int64 = 256 << 20
	defaultMaxInflightRequestBodyBytes int64 = 1 << 30
	responsesHTTPRequestBodyCopies     int64 = 2
	chatHTTPRequestBodyCopies          int64 = 4
	responsesWSRequestBodyCopies       int64 = 4
	maxHTTPRequestBodyCopies                 = chatHTTPRequestBodyCopies
	maxResponsesWSEventBytes                 = 32 << 20
	defaultRequestBodyReadTimeout            = 5 * time.Minute
)

var (
	errRequestBodyTooLarge = errors.New("request body is too large")
	errRequestBodyCapacity = errors.New("request body capacity is unavailable")
)

type byteBudget struct {
	limit int64
	used  atomic.Int64
}

func newByteBudget(limit int64) *byteBudget {
	return &byteBudget{limit: limit}
}

func (b *byteBudget) tryAcquire(bytes int64) bool {
	if bytes < 0 || bytes > b.limit {
		return false
	}
	for {
		used := b.used.Load()
		if bytes > b.limit-used {
			return false
		}
		if b.used.CompareAndSwap(used, used+bytes) {
			return true
		}
	}
}

func (b *byteBudget) release(bytes int64) {
	if bytes <= 0 {
		return
	}
	if remaining := b.used.Add(-bytes); remaining < 0 {
		panic("request body byte budget released more bytes than reserved")
	}
}

type bodyReservation struct {
	budget   *byteBudget
	bytes    int64
	released atomic.Bool
}

func (r *bodyReservation) resize(bytes int64) {
	if bytes >= r.bytes {
		return
	}
	r.budget.release(r.bytes - bytes)
	r.bytes = bytes
}

func (r *bodyReservation) grow(bytes int64) bool {
	if bytes <= r.bytes {
		return true
	}
	if !r.budget.tryAcquire(bytes - r.bytes) {
		return false
	}
	r.bytes = bytes
	return true
}

func (r *bodyReservation) release() {
	if r.released.CompareAndSwap(false, true) {
		r.budget.release(r.bytes)
	}
}

func requestBodyCost(bytes, copies int64) (int64, bool) {
	if bytes < 0 || copies <= 0 || bytes > math.MaxInt64/copies {
		return 0, false
	}
	return bytes * copies, true
}

func (s *Server) readRequest(w http.ResponseWriter, r *http.Request) ([]byte, requestMeta, func(), bool) {
	copies := responsesHTTPRequestBodyCopies
	if r.URL.Path == "/v1/chat/completions" {
		copies = chatHTTPRequestBodyCopies
	}
	body, reservation, err := s.readBufferedBody(r.Body, r.ContentLength, copies)
	if errors.Is(err, errRequestBodyTooLarge) {
		slog.Warn("request body rejected", "reason", "too_large", "path", r.URL.Path, "content_length", r.ContentLength, "max_bytes", s.maxRequestBodyBytes)
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body is too large", "invalid_request_error")
		return nil, requestMeta{}, nil, false
	}
	if errors.Is(err, errRequestBodyCapacity) {
		slog.Warn("request body rejected", "reason", "capacity", "path", r.URL.Path, "content_length", r.ContentLength,
			"inflight_buffer_bytes", s.requestBodyBudget.used.Load(), "max_inflight_buffer_bytes", s.requestBodyBudget.limit)
		w.Header().Set("Retry-After", "1")
		writeOpenAIError(w, http.StatusServiceUnavailable, "request body capacity is temporarily unavailable", "subpool_capacity_exceeded")
		return nil, requestMeta{}, nil, false
	}
	if err != nil {
		slog.Warn("request body rejected", "reason", "read_failed", "path", r.URL.Path, "content_length", r.ContentLength, "error", err)
		writeOpenAIError(w, http.StatusBadRequest, "request body could not be read", "invalid_request_error")
		return nil, requestMeta{}, nil, false
	}
	var meta requestMeta
	if json.Unmarshal(body, &meta) != nil || strings.TrimSpace(meta.Model) == "" {
		reservation.release()
		writeOpenAIError(w, http.StatusBadRequest, "model is required", "invalid_request_error")
		return nil, requestMeta{}, nil, false
	}
	return body, meta, reservation.release, true
}

func (s *Server) readBufferedBody(reader io.Reader, declaredBytes, copies int64) ([]byte, *bodyReservation, error) {
	if declaredBytes > s.maxRequestBodyBytes {
		return nil, nil, errRequestBodyTooLarge
	}
	reservedCost := int64(0)
	if declaredBytes >= 0 {
		var valid bool
		reservedCost, valid = requestBodyCost(declaredBytes, copies)
		if !valid || !s.requestBodyBudget.tryAcquire(reservedCost) {
			return nil, nil, errRequestBodyCapacity
		}
	}
	reservation := &bodyReservation{budget: s.requestBodyBudget, bytes: reservedCost}
	body := make([]byte, 0)
	buffer := make([]byte, 64<<10)
	emptyReads := 0
	for {
		remaining := s.maxRequestBodyBytes - int64(len(body))
		if remaining == 0 {
			var extra [1]byte
			count, err := reader.Read(extra[:])
			if count > 0 {
				reservation.release()
				return nil, nil, errRequestBodyTooLarge
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				reservation.release()
				return nil, nil, err
			}
			emptyReads++
			if emptyReads >= 100 {
				reservation.release()
				return nil, nil, io.ErrNoProgress
			}
			continue
		}
		readSize := int64(len(buffer))
		if remaining < readSize {
			readSize = remaining
		}
		targetCost, valid := requestBodyCost(int64(len(body))+readSize, copies)
		if !valid || !reservation.grow(targetCost) {
			reservation.release()
			return nil, nil, errRequestBodyCapacity
		}
		count, err := reader.Read(buffer[:readSize])
		body = append(body, buffer[:count]...)
		if declaredBytes < 0 {
			observedCost, _ := requestBodyCost(int64(len(body)), copies)
			reservation.resize(observedCost)
		}
		if count > 0 {
			emptyReads = 0
		} else if err == nil {
			emptyReads++
			if emptyReads >= 100 {
				reservation.release()
				return nil, nil, io.ErrNoProgress
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			reservation.release()
			return nil, nil, err
		}
	}
	observedCost, _ := requestBodyCost(int64(len(body)), copies)
	reservation.resize(observedCost)
	return body, reservation, nil
}
