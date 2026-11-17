package openaibilling

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPromotionalBudgetFailsClosed(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name            string
		remaining, paid float64
		expiry, age     time.Duration
		quotaError      string
		want            bool
	}{
		{"valid estimate", 10, 0, time.Hour, time.Hour, "", true},
		{"bounded rate limit", 10, 0, time.Hour, 3 * time.Hour, "quota_probe_rate_limited", true},
		{"expired", 10, 0, -time.Second, 0, "", false},
		{"exhausted", 0, 0, time.Hour, 0, "", false},
		{"purchased balance", 10, 1, time.Hour, 0, "", false},
		{"stale", 10, 0, time.Hour, 25 * time.Hour, "", false},
		{"auth failure", 10, 0, time.Hour, 0, "authorization_expired", false},
		{"future check", 10, 0, time.Hour, -time.Minute, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(Snapshot{PlanType: PlanType, Balance: Balance{Granted: 20, Remaining: tc.remaining, PaidAvailable: tc.paid, ExpiresAt: now.Add(tc.expiry).Unix(), Estimated: true}})
			checked := now.Add(-tc.age)
			if _, ok := PromotionalBudget(raw, &checked, tc.quotaError, now); ok != tc.want {
				t.Fatalf("eligible=%v", ok)
			}
			if _, ok := PromotionalBudget(raw, nil, "", now); ok {
				t.Fatal("missing check accepted")
			}
		})
	}
}
