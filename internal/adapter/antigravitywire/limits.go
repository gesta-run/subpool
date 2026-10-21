package antigravitywire

import (
	"sync"
	"time"
)

type ModelLimits struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func NewModelLimits() *ModelLimits { return &ModelLimits{until: make(map[string]time.Time)} }

func (m *ModelLimits) Set(scope, model string, delay time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, until := range m.until {
		if time.Now().After(until) || len(m.until) >= 4096 {
			delete(m.until, key)
		}
	}
	m.until[signatureKey(scope, model, "")] = time.Now().Add(min(max(time.Second, delay), 15*time.Minute))
}

func (m *ModelLimits) Remaining(scope, model string) time.Duration {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return max(0, time.Until(m.until[signatureKey(scope, model, "")]))
}
