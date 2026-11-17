package store

import (
	"encoding/json"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"testing"
	"time"
)

func testPromotionalQuota(now time.Time, remaining float64, expires time.Duration) []byte {
	raw, _ := json.Marshal(openaibilling.Snapshot{PlanType: openaibilling.PlanType, Balance: openaibilling.Balance{Granted: 50, Remaining: remaining, ExpiresAt: now.Add(expires).Unix(), Estimated: true}})
	return raw
}

func TestPromotionalRoutingThreeTiers(t *testing.T) {
	now := time.Now()
	sub, _ := quotaRoutingBudget(testCreditQuota(now, 99, "500"), &now, "", now)
	credits, _ := quotaRoutingBudget(testCreditQuota(now, 100, "500"), &now, "", now)
	api, ok := quotaRoutingBudget(testPromotionalQuota(now, 5, time.Hour), &now, "", now)
	if !ok || !api.apiCredits {
		t.Fatal("valid grant rejected")
	}
	later, _ := quotaRoutingBudget(testPromotionalQuota(now, 40, 2*time.Hour), &now, "", now)
	if !sub.preferredTo(api, 100, 0, true) || api.preferredTo(sub, 0, 100, true) {
		t.Fatal("subscription tier lost")
	}
	if !api.preferredTo(credits, 100, 0, true) || credits.preferredTo(api, 0, 100, true) {
		t.Fatal("grant tier lost")
	}
	if !api.preferredTo(later, 100, 0, true) || later.preferredTo(api, 0, 100, true) {
		t.Fatal("earliest expiry lost")
	}
	for _, raw := range [][]byte{testPromotionalQuota(now, 0, time.Hour), testPromotionalQuota(now, 5, -time.Second)} {
		if _, ok := quotaRoutingBudget(raw, &now, "", now); ok {
			t.Fatal("invalid grant accepted")
		}
	}
}
