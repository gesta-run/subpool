// Package routingstats observes existing routing boundaries without changing them.
package routingstats

import (
	"fmt"
	"sync/atomic"
)

// Counters are process-local and use fixed labels only. They contain no request,
// employee, credential, or response identifiers. An unbound request is a routing
// opportunity under the current contract, not a guarantee that replay will work.
type Counters struct {
	unbound         atomic.Uint64
	continuation    atomic.Uint64
	connectionBound atomic.Uint64
}

func (c *Counters) Record(continuation, connectionBound bool) {
	switch {
	case continuation:
		c.continuation.Add(1)
	case connectionBound:
		c.connectionBound.Add(1)
	default:
		c.unbound.Add(1)
	}
}

func (c *Counters) Metrics(transport string) string {
	return fmt.Sprintf("subpool_codex_routing_requests_total{transport=%q,binding=\"unbound\"} %d\n"+
		"subpool_codex_routing_requests_total{transport=%q,binding=\"continuation\"} %d\n"+
		"subpool_codex_routing_requests_total{transport=%q,binding=\"connection\"} %d\n",
		transport, c.unbound.Load(), transport, c.continuation.Load(), transport, c.connectionBound.Load())
}
