package openaibilling

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPromotionalBudgetFailsClosed(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name             string
		remaining, paid  float64
		expiry, age      time.Duration
		quotaError       string
		verifiedDepleted bool
		want             bool
	}{
		{"valid estimate", 10, 0, time.Hour, time.Hour, "", false, true},
		{"bounded rate limit", 10, 0, time.Hour, 3 * time.Hour, "quota_probe_rate_limited", false, true},
		{"verified depletion overrides a positive estimate", 10, 0, time.Hour, 0, "", true, false},
		{"expired", 10, 0, -time.Second, 0, "", false, false},
		{"exhausted", 0, 0, time.Hour, 0, "", false, false},
		{"purchased balance", 10, 1, time.Hour, 0, "", false, false},
		{"stale", 10, 0, time.Hour, 25 * time.Hour, "", false, false},
		{"auth failure", 10, 0, time.Hour, 0, "authorization_expired", false, false},
		{"future check", 10, 0, time.Hour, -time.Minute, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := Snapshot{PlanType: PlanType, Balance: Balance{Granted: 20, Remaining: tc.remaining, PaidAvailable: tc.paid, ExpiresAt: now.Add(tc.expiry).Unix(), Estimated: true}}
			if tc.verifiedDepleted {
				snapshot.GrantDepletedAt = now.Add(-time.Minute).Unix()
			}
			raw, _ := json.Marshal(snapshot)
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
