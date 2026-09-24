package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
)

func TestAccountHealthyRejectsFullUnresetQuotaWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	allowed := true
	quota, _ := json.Marshal(codex.UsageSnapshot{
		UsageAllowed: &allowed,
		Weekly:       &codex.UsageWindow{UsedPercent: 100, ResetAt: now.Add(time.Hour).Unix()},
	})
	account := domain.ProviderAccount{Status: domain.AccountActive, HealthStatus: domain.HealthHealthy, QuotaSnapshot: quota}
	if accountHealthy(account, now) {
		t.Fatal("full quota window was considered healthy")
	}
	account.QuotaSnapshot, _ = json.Marshal(codex.UsageSnapshot{
		UsageAllowed: &allowed,
		Weekly:       &codex.UsageWindow{UsedPercent: 100, ResetAt: now.Add(-time.Hour).Unix()},
	})
	if !accountHealthy(account, now) {
		t.Fatal("reset quota window was considered unavailable")
	}
}

func TestRetryAfterCapsProviderQuotaWindow(t *testing.T) {
	now := time.Date(2026, 9, 16, 4, 22, 0, 0, time.UTC)
	cases := map[string]struct {
		value string
		want  time.Time
	}{
		"missing":              {"", now.Add(time.Minute)},
		"short seconds":        {"30", now.Add(30 * time.Second)},
		"weekly reset seconds": {"461494", now.Add(maxRateLimitCooldown)},
		"weekly reset date":    {now.Add(5 * 24 * time.Hour).Format(http.TimeFormat), now.Add(maxRateLimitCooldown)},
		"past date":            {now.Add(-time.Hour).Format(http.TimeFormat), now.Add(-time.Hour)},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got := retryAfter(http.Header{"Retry-After": []string{test.value}}, now)
			if !got.Equal(test.want) {
				t.Fatalf("retryAfter(%q) = %s, want %s", test.value, got, test.want)
			}
		})
	}
}
