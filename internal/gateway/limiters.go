package gateway

import (
	"sync"
	"time"
)

const requestActivityTTL = time.Minute

type requestActivityThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newRequestActivityThrottle() *requestActivityThrottle {
	return &requestActivityThrottle{last: make(map[string]time.Time)}
}

func (t *requestActivityThrottle) ShouldRecord(accountID, keyID string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := accountID + "\x00" + keyID
	if last := t.last[key]; !last.IsZero() && now.Sub(last) < requestActivityTTL {
		return false
	}
	t.last[key] = now
	if len(t.last) > 10_000 {
		cutoff := now.Add(-2 * requestActivityTTL)
		for candidate, last := range t.last {
			if last.Before(cutoff) {
				delete(t.last, candidate)
			}
		}
	}
	return true
}
