package codex

import (
	"testing"
	"time"
)

func TestPrepaidCreditsRequireAvailableFiniteBalance(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "NaN", "+Inf", "invalid", "12.5"} {
		credits := &CreditsSnapshot{HasCredits: true, Balance: &value}
		got := credits.PrepaidBalance()
		if (got > 0) != (value == "12.5") {
			t.Fatalf("balance %q accepted as %v", value, got)
		}
	}
	value := "1000"
	for _, credits := range []*CreditsSnapshot{nil, {HasCredits: false, Balance: &value}, {HasCredits: true, Unlimited: true, Balance: &value}, {HasCredits: true}} {
		if credits.PrepaidBalance() != 0 {
			t.Fatal("unverified prepaid balance accepted")
		}
	}
}

func TestCreditsContinueAfterSubscriptionButNotExplicitDenial(t *testing.T) {
	now := time.Now()
	balance := "1000"
	allowed := true
	snapshot := UsageSnapshot{UsageAllowed: &allowed, Weekly: &UsageWindow{UsedPercent: 100, ResetAt: now.Add(time.Hour).Unix()}, Credits: &CreditsSnapshot{HasCredits: true, Balance: &balance}}
	if !snapshot.AllowsUsageAt(now) {
		t.Fatal("available credits blocked by subscription window")
	}
	allowed = false
	if snapshot.AllowsUsageAt(now) {
		t.Fatal("upstream denial overridden by credits")
	}
}

func TestNormalizeCreditsAndSpendControls(t *testing.T) {
	balance := "1000"
	spend := true
	reason := "credits_exhausted"
	for _, test := range []struct {
		name    string
		limits  appServerRateLimits
		allowed bool
	}{
		{"prepaid continuation", appServerRateLimits{}, true},
		{"spend limit", appServerRateLimits{SpendControlReached: &spend}, false},
		{"upstream denial", appServerRateLimits{RateLimitReachedType: &reason}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.limits.Secondary = &appServerRateLimitWindow{UsedPercent: 100}
			test.limits.Credits = &appServerCreditsSnapshot{HasCredits: true, Balance: &balance}
			snapshot := normalizeAppServerRateLimits(test.limits)
			if snapshot.UsageAllowed == nil || *snapshot.UsageAllowed != test.allowed {
				t.Fatalf("snapshot=%+v", snapshot)
			}
		})
	}
}
