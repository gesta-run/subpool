package store

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/provider/codex"
)

func testQuota(now time.Time, fiveHourUsed, weeklyUsed float64) []byte {
	raw, _ := json.Marshal(codex.UsageSnapshot{PlanType: "test-plan",
		FiveHour: &codex.UsageWindow{UsedPercent: fiveHourUsed, ResetAt: now.Add(time.Hour).Unix()},
		Weekly:   &codex.UsageWindow{UsedPercent: weeklyUsed, ResetAt: now.Add(24 * time.Hour).Unix()},
	})
	return raw
}

func testQuotaWithWindow(now time.Time, name string, used float64) []byte {
	raw, _ := json.Marshal(map[string]any{"plan_type": "test-plan", name: &codex.UsageWindow{UsedPercent: used, ResetAt: now.Add(time.Hour).Unix()}})
	return raw
}

func TestQuotaRoutingScore(t *testing.T) {
	now := time.Now()
	stale := now.Add(-16 * time.Minute)
	future := now.Add(time.Minute)
	for _, tt := range []struct {
		name       string
		raw        []byte
		checked    *time.Time
		quotaError string
		want       float64
		valid      bool
	}{
		{"weekly bottleneck", testQuota(now, 10, 70), &now, "", 30, true},
		{"short bottleneck", testQuota(now, 80, 20), &now, "", 20, true},
		{"weekly only", testQuotaWithWindow(now, "weekly", 35), &now, "", 65, true},
		{"five hour only", testQuotaWithWindow(now, "five_hour", 25), &now, "", 75, true},
		{"no windows", []byte(`{"plan_type":"test-plan"}`), &now, "", 0, false},
		{"unknown window", testQuotaWithWindow(now, "other", 25), &now, "", 0, false},
		{"weekly only reset passed", testQuotaWithWindow(now.Add(-2*time.Hour), "weekly", 35), &now, "", 0, false},
		{"missing check", testQuota(now, 10, 20), nil, "", 0, false},
		{"stale", testQuota(now, 10, 20), &stale, "", 0, false},
		{"future", testQuota(now, 10, 20), &future, "", 0, false},
		{"probe failed", testQuota(now, 10, 20), &now, "probe_failed", 0, false},
		{"reset passed", testQuota(now.Add(-2*time.Hour), 10, 20), &now, "", 0, false},
		{"missing window", []byte(`{"weekly":{"used_percent":20}}`), &now, "", 0, false},
		{"missing used percentage", []byte(`{"plan_type":"test-plan","five_hour":{"reset_at":9999999999},"weekly":{"used_percent":10,"reset_at":9999999999}}`), &now, "", 0, false},
		{"invalid", []byte(`{`), &now, "", 0, false},
		{"out of range", testQuota(now, -1, 20), &now, "", 0, false},
		{"exhausted", testQuota(now, 100, 20), &now, "", 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			budget, valid := quotaRoutingBudget(tt.raw, tt.checked, tt.quotaError, now)
			if budget.remaining != tt.want || valid != tt.valid {
				t.Fatalf("score=%v valid=%v", budget.remaining, valid)
			}
		})
	}
}

func testCreditQuota(now time.Time, used float64, balance string) []byte {
	allowed := true
	raw, _ := json.Marshal(codex.UsageSnapshot{PlanType: "illustrative-plan", UsageAllowed: &allowed, Weekly: &codex.UsageWindow{UsedPercent: used, ResetAt: now.Add(time.Hour).Unix()}, Credits: &codex.CreditsSnapshot{HasCredits: true, Balance: &balance}})
	return raw
}

func TestQuotaRoutingSubscriptionBeforeCredits(t *testing.T) {
	now := time.Now()
	subscription, valid := quotaRoutingBudget(testCreditQuota(now, 99, "1000"), &now, "", now)
	if !valid || subscription.credits || subscription.remaining != 1 {
		t.Fatalf("subscription=%+v", subscription)
	}
	credits, valid := quotaRoutingBudget(testCreditQuota(now, 100, "2000"), &now, "", now)
	if !valid || !credits.credits || credits.remaining != 2000 {
		t.Fatalf("credits=%+v", credits)
	}
	if !subscription.preferredTo(credits, 10, 0, true) || credits.preferredTo(subscription, 0, 10, true) {
		t.Fatal("priority or balance overrode subscription tier")
	}
	more := credits
	more.remaining++
	if !more.preferredTo(credits, 0, 0, true) {
		t.Fatal("credits incorrectly use subscription percentage margin")
	}
	for _, balance := range []string{"0", "-1", "invalid", "NaN"} {
		if _, ok := quotaRoutingBudget(testCreditQuota(now, 100, balance), &now, "", now); ok {
			t.Fatalf("unusable credits accepted: %q", balance)
		}
	}
	stale := now.Add(-time.Hour)
	if _, ok := quotaRoutingBudget(testCreditQuota(now, 100, "2000"), &stale, "", now); ok {
		t.Fatal("stale credits accepted")
	}
}
