package reporting

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReportCodexCreditsAndNearestExpiry(t *testing.T) {
	c := fixtureConfig()
	now := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	expiry := now.Add(11*time.Hour + 30*time.Minute)
	resets, _ := json.Marshal(map[string]any{
		"available_count": 3,
		"credits": []map[string]any{
			{"status": "available", "expires_at": now.Add(5 * 24 * time.Hour).Unix()},
			{"status": "used", "expires_at": now.Add(time.Hour).Unix()},
			{"status": "available", "expires_at": expiry.Unix()},
			{"status": "available", "expires_at": nil},
		},
	})
	// All account identities, counts, balances and dates are illustrative.
	a := Account{Name: "Illustrative account", Email: "account@example.com", Provider: "codex", CredentialType: "subscription", Requests: 250,
		Snapshot: []byte(`{"weekly":{"remaining_percent":75,"reset_at":2000000000},"credits":{"has_credits":true,"balance":"12345.4","unlimited":false}}`), CheckedAt: now,
		ResetSnapshot: resets, ResetCheckedAt: now}
	r := NewReport(c, []Account{a}, now.Add(-time.Hour), now, now, false, 0)
	row := r.Rows[0]
	if row.AICredits != "12,345" || row.ResetCredits != "3" || !row.ExpirySoon || row.CreditExpiry != "Next reset expires 2026-01-03 · 04:30 (in 12 hours)" {
		t.Fatalf("incorrect credit summary: %+v", row)
	}
	m, err := Render(c, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{m.HTML, m.Text} {
		for _, want := range []string{"12,345", "AI credits", "3", "reset credits", row.CreditExpiry} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q", want)
			}
		}
	}
	if path := os.Getenv("SUBPOOL_CREDITS_EMAIL_PREVIEW"); path != "" {
		if err := os.WriteFile(path, []byte(m.HTML), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, snapshot, resets  string
		staleQuota, staleResets bool
		credits, count          string
	}{
		{"explicit zero", `{"credits":{"balance":"0"}}`, `{"available_count":0,"credits":[]}`, false, false, "0", "0"},
		{"unlimited", `{"credits":{"unlimited":true}}`, `{"available_count":0}`, false, false, "Unlimited", "0"},
		{"missing values", `{}`, `{}`, false, false, "Unavailable", "Unavailable"},
		{"invalid values", `{"credits":{"balance":"NaN"}}`, `{"available_count":-1}`, false, false, "Unavailable", "Unavailable"},
		{"stale quota only", `{"credits":{"balance":"42"}}`, `{"available_count":0}`, true, false, "Unavailable", "0"},
		{"stale resets only", `{"credits":{"balance":"42"}}`, `{"available_count":0}`, false, true, "42", "Unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := a
			account.Snapshot, account.ResetSnapshot = []byte(tc.snapshot), []byte(tc.resets)
			if tc.staleQuota {
				account.CheckedAt = now.Add(-16 * time.Minute)
			}
			if tc.staleResets {
				account.ResetCheckedAt = now.Add(-16 * time.Minute)
			}
			got := NewReport(c, []Account{account}, now.Add(-time.Hour), now, now, false, 0).Rows[0]
			if got.AICredits != tc.credits || got.ResetCredits != tc.count || got.CreditExpiry != "" || got.ExpirySoon {
				t.Fatalf("incorrect summary: %+v", got)
			}
		})
	}
	for _, provider := range []string{"openai_compatible", "copilot"} {
		a.Provider = provider
		row := NewReport(c, []Account{a}, now.Add(-time.Hour), now, now, false, 0).Rows[0]
		if row.AICredits != "" || row.ResetCredits != "" {
			t.Fatal("unsupported provider shows Codex credits")
		}
	}
}
