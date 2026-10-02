package clientquota

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

func TestCopilotCreditsMapToCodexHeadersAndEvent(t *testing.T) {
	quota, err := json.Marshal(copilot.CreditsSnapshot{Credits: &copilot.CreditsQuota{
		Entitlement: 1500, Remaining: 1125, RemainingPercent: 75,
	}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1800000000, 0)
	account := domain.ProviderAccount{Provider: domain.ProviderCopilot, QuotaSnapshot: quota, QuotaCheckedAt: &now}
	header := make(http.Header)
	ApplyHeaders(header, account, now)
	if header.Get(hasCreditsHeader) != "true" || header.Get(unlimitedHeader) != "false" || header.Get(balanceHeader) != "1125" {
		t.Fatalf("headers = %#v", header)
	}
	var event map[string]any
	if err = json.Unmarshal(EventFromHeaders(header, "worker-1"), &event); err != nil {
		t.Fatal(err)
	}
	credits, _ := event["credits"].(map[string]any)
	if event["type"] != "codex.rate_limits" || event["stream_id"] != "worker-1" || credits["has_credits"] != true || credits["balance"] != "1125" {
		t.Fatalf("event = %#v", event)
	}
}

func TestCopilotCreditsHandleUnlimitedAndUnavailableSnapshots(t *testing.T) {
	quota, _ := json.Marshal(copilot.CreditsSnapshot{Credits: &copilot.CreditsQuota{Unlimited: true}})
	now := time.Unix(1800000000, 0)
	account := domain.ProviderAccount{Provider: domain.ProviderCopilot, QuotaSnapshot: quota, QuotaCheckedAt: &now}
	header := make(http.Header)
	ApplyHeaders(header, account, now)
	if header.Get(hasCreditsHeader) != "false" || header.Get(unlimitedHeader) != "true" || header.Get(balanceHeader) != "" {
		t.Fatalf("headers = %#v", header)
	}
	if _, ok := FromAccount(domain.ProviderAccount{Provider: domain.ProviderCodex, QuotaSnapshot: quota, QuotaCheckedAt: &now}, now); ok {
		t.Fatal("Codex account produced synthetic Copilot credits")
	}
	if _, ok := FromAccount(domain.ProviderAccount{Provider: domain.ProviderCopilot, QuotaSnapshot: json.RawMessage(`{"credits":`), QuotaCheckedAt: &now}, now); ok {
		t.Fatal("invalid quota produced credits")
	}
	if payload := EventFromHeaders(http.Header{hasCreditsHeader: {"invalid"}, unlimitedHeader: {"false"}}, ""); payload != nil {
		t.Fatalf("invalid headers produced event: %s", payload)
	}
}

func TestCopilotCreditsRejectStaleOrFailedQuotaChecks(t *testing.T) {
	now := time.Unix(1800000000, 0)
	checkedAt := now.Add(-maxSnapshotAge - time.Second)
	quota, _ := json.Marshal(copilot.CreditsSnapshot{Credits: &copilot.CreditsQuota{Remaining: 25}})
	account := domain.ProviderAccount{Provider: domain.ProviderCopilot, QuotaSnapshot: quota, QuotaCheckedAt: &checkedAt}
	if _, ok := FromAccount(account, now); ok {
		t.Fatal("stale quota produced credits")
	}
	checkedAt = now
	account.LastQuotaErrorCode = "quota_probe_rate_limited"
	if _, ok := FromAccount(account, now); ok {
		t.Fatal("failed quota check produced credits")
	}
}
