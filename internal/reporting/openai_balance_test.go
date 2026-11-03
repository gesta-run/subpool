package reporting

import (
	"strings"
	"testing"
	"time"
)

func TestEmailShowsRealDollarBalanceAndLabelsStaleAuthorization(t *testing.T) {
	// All values are illustrative and unrelated to live accounts.
	now := time.Unix(1800000000, 0)
	account := Account{Name: "Example API", CheckedAt: now, Snapshot: []byte(`{"plan_type":"openai_api_credits","balance":{"remaining_usd":37.5,"granted_usd":50,"used_usd":12.5,"expires_at":1800001000}}`)}
	for _, stale := range []bool{false, true} {
		if stale {
			account.QuotaError = "quota_authorization_expired"
			account.CheckedAt = now.Add(-time.Hour)
		}
		r := NewReport(fixtureConfig(), []Account{account}, now.Add(-time.Hour), now, now, false, 0)
		m, err := Render(fixtureConfig(), r)
		if err != nil {
			t.Fatal(err)
		}
		for _, body := range []string{m.HTML, m.Text} {
			if !strings.Contains(body, "$37.50") || !strings.Contains(body, "Credits expire") || !strings.Contains(body, "Updated") {
				t.Fatal("balance details missing")
			}
			if stale && !strings.Contains(body, "Last known balance; reconnect billing") {
				t.Fatal("stale amount presented as current")
			}
		}
		if !r.Rows[0].HideBar {
			t.Fatal("dollar balance rendered as percentage")
		}
	}
	account.QuotaError = ""
	account.CheckedAt = now
	account.Snapshot = []byte(`{"plan_type":"openai_api_credits","balance":{"remaining_usd":0}}`)
	r := NewReport(fixtureConfig(), []Account{account}, now, now, now, false, 0)
	if r.Rows[0].Remaining != "$0.00" {
		t.Fatal("zero balance treated as unknown")
	}
}

func TestMissingBalanceIsNeverReportedAsZero(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, raw := range []string{`{"plan_type":"openai_api_credits"}`, `{"plan_type":"openai_api_credits","balance":{}}`} {
		row := Row{Remaining: "Unavailable"}
		if !fillOpenAIBalance(&row, Account{Snapshot: []byte(raw), CheckedAt: now}, now, time.UTC) || row.Remaining != "Unavailable" {
			t.Fatal("missing balance became zero")
		}
	}
}

func TestEstimatedBalanceIsLabeledInBothEmailFormats(t *testing.T) {
	now := time.Unix(1800200000, 0)
	account := Account{Name: "Illustrative API", CheckedAt: now, Snapshot: []byte(`{"plan_type":"openai_api_credits","balance":{"remaining_usd":37.5,"estimated":true,"usage_through":1800172800,"expires_at":1800600000}}`)}
	r := NewReport(fixtureConfig(), []Account{account}, now.Add(-time.Hour), now, now, false, 0)
	m, err := Render(fixtureConfig(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{m.HTML, m.Text} {
		if !strings.Contains(body, "Estimated") || !strings.Contains(body, "Costs through") || !strings.Contains(body, "$37.50") {
			t.Fatal("estimate source or amount missing")
		}
	}
}
