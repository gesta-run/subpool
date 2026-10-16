package gateway

import "context"

type compactBodyReservationKey struct{}

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
