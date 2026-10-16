package gateway

import (
	"context"
	"io"
)

type compactBodyReservationKey struct{}

type compactResponseBody struct {
	io.ReadCloser
	release func()
}

func (body *compactResponseBody) Close() error {
	defer body.release()
	return body.ReadCloser.Close()
}

func (s *Server) reserveCompactionTranscript(ctx context.Context, bodyBytes int) (func(), error) {
	if ctx.Value(compactBodyReservationKey{}) != nil {
		return reserveCompactionTranscript(ctx, bodyBytes)
	}
	// WebSocket turns hold their own admission lease outside this context.
	// Reserve additional work buffers without mutating that concurrent lease.
	limits, err := s.requestBodyLimits(ctx)
	if err != nil {
		return nil, err
	}
	cost, valid := requestBodyCost(int64(bodyBytes), compactHTTPRequestBodyCopies)
	if !valid || !s.requestBodyBudget.tryAcquireWithin(cost, limits.MaxInflightRequestBodyBytes) {
		return nil, errRequestBodyCapacity
	}
	return func() { s.requestBodyBudget.release(cost) }, nil
}

// Reserve transcript buffers only after routing to Copilot. Native compaction
// keeps its original admission cost, including in mixed-provider pools.
func reserveCompactionTranscript(ctx context.Context, bodyBytes int) (func(), error) {
	reservation, _ := ctx.Value(compactBodyReservationKey{}).(*bodyReservation)
	if reservation == nil {
		return nil, errRequestBodyCapacity
	}
	cost, valid := requestBodyCost(int64(bodyBytes), compactHTTPRequestBodyCopies)
	previous := reservation.bytes
	if !valid || !reservation.grow(cost) {
		return nil, errRequestBodyCapacity
	}
	return func() { reservation.resize(previous) }, nil
}
