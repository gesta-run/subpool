package reporting

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureConfig() Config {
	location, _ := time.LoadLocation("Asia/Shanghai")
	return Config{Enabled: true, APIKey: "test-key", From: "SubPool <reports@example.com>", To: []string{"team@example.com"}, PublicURL: "https://pool.example.com", Location: location, Hour: 17, Minute: 50}
}

func TestRenderTeamUsage(t *testing.T) {
	c := fixtureConfig()
	now := time.Date(2026, 1, 2, 9, 50, 0, 0, time.UTC)
	// All account counts and capacities in this fixture are illustrative.
	accounts := []Account{{Name: "Codex 1", Requests: 1280, Snapshot: []byte(`{"weekly":{"remaining_percent":72,"reset_at":2000000000}}`), CheckedAt: now}, {Name: "Codex 2", Requests: 960, Snapshot: []byte(`{"weekly":{"remaining_percent":64,"reset_at":2000000000}}`), CheckedAt: now}}
	report := NewReport(c, accounts, now.Add(-17*time.Hour-50*time.Minute), now, now, false)
	message, err := Render(c, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Team usage overview", "1,280", "57.1%", "42.9%", "72%", "64%"} {
		if !strings.Contains(message.HTML, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{"of team requests", "Your daily", "DESIGN PREVIEW"} {
		if strings.Contains(message.HTML, unwanted) {
			t.Errorf("unexpected %q", unwanted)
		}
	}
	if report.Total != 2240 {
		t.Fatal(report.Total)
	}
	if path := os.Getenv("SUBPOOL_EMAIL_PREVIEW"); path != "" {
		if err = os.WriteFile(path, []byte(message.HTML), 0600); err != nil {
			t.Fatal(err)
		}
	}
	accounts[0].CheckedAt = now.Add(-16 * time.Minute)
	stale := NewReport(c, accounts, now.Add(-time.Hour), now, now, false)
	if stale.Rows[0].Remaining != "Unavailable" {
		t.Fatal("stale quota was reported as current")
	}
	accounts[0].CheckedAt = now
	accounts[0].Snapshot = []byte(`{"weekly":{"reset_at":2000000000}}`)
	missing := NewReport(c, accounts, now.Add(-time.Hour), now, now, false)
	if missing.Rows[0].Remaining != "Unavailable" {
		t.Fatal("missing quota was shown as zero")
	}
	empty := NewReport(c, nil, now.Add(-time.Hour), now, now, false)
	if empty.Total != 0 {
		t.Fatal("empty report has requests")
	}
}

func TestReportEmailResetAndPartialWindow(t *testing.T) {
	c := fixtureConfig()
	now := time.Date(2026, 1, 2, 9, 50, 0, 0, time.UTC)
	reset := now.Add(5*24*time.Hour + 20*time.Hour)
	snapshot, _ := json.Marshal(map[string]any{"weekly": map[string]any{"remaining_percent": 72, "reset_at": reset.Unix()}})
	// This account address and capacity are illustrative.
	accounts := []Account{{Name: "Internal label", Email: "account@example.com", Snapshot: snapshot, CheckedAt: now}}
	report := NewReport(c, accounts, now.Add(-17*time.Minute), now, now, true)
	if report.Rows[0].Name != "account@example.com" {
		t.Fatal("account email was not used")
	}
	if report.Rows[0].Reset != "Resets 2026-01-08 · 13:50 (in 6 days)" {
		t.Fatal(report.Rows[0].Reset)
	}
	message, err := Render(c, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{message.HTML, message.Text} {
		for _, want := range []string{"account@example.com", "17:33–17:50", "Partial first-day report", "(in 6 days)"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q", want)
			}
		}
	}
	for _, tc := range []struct {
		remaining time.Duration
		label     string
	}{
		{24 * time.Hour, "(in 1 day)"},
		{2*time.Hour + time.Minute, "(in 3 hours)"},
		{time.Hour, "(in 1 hour)"},
		{30 * time.Second, "(in 1 minute)"},
	} {
		if got := resetLabel(now.Add(tc.remaining), now, c.Location); !strings.HasSuffix(got, tc.label) {
			t.Fatal(got)
		}
	}
}

type fakeStore struct {
	payload []byte
	sent    bool
	retry   bool
	count   int
}

func (f *fakeStore) ReportTrackingStarted(context.Context) (time.Time, error) {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), nil
}
func (f *fakeStore) ReportAccounts(context.Context, time.Time, time.Time) ([]Account, error) {
	return []Account{{Name: "Codex 1", Requests: int64(f.count)}}, nil
}
func (f *fakeStore) ClaimDailyReport(_ context.Context, _ string, p []byte) ([]byte, bool, error) {
	if f.sent {
		return nil, false, nil
	}
	if f.payload == nil {
		f.payload = append([]byte(nil), p...)
	}
	return f.payload, true, nil
}
func (f *fakeStore) FinishDailyReport(context.Context, string, string) error {
	f.sent = true
	return nil
}
func (f *fakeStore) RetryDailyReport(context.Context, string) error { f.retry = true; return nil }

func TestScheduleRetryFreezesPayloadAndRestartDoesNotResend(t *testing.T) {
	var calls int
	var bodies, keys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{"id":"test-email"}`)
	}))
	defer server.Close()
	st := &fakeStore{count: 10}
	s := &Service{Config: fixtureConfig(), Store: st, Client: server.Client(), Endpoint: server.URL}
	cutoff := time.Date(2026, 1, 2, 9, 50, 0, 0, time.UTC)
	if err := s.SendDue(context.Background(), cutoff.Add(-time.Second)); err != nil || calls != 0 {
		t.Fatal("sent before cutoff", err)
	}
	if err := s.SendDue(context.Background(), cutoff); err == nil || !st.retry {
		t.Fatal("failed send was not retriable")
	}
	st.count = 999
	if err := s.SendDue(context.Background(), cutoff.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var frozen Message
	if err := json.Unmarshal(st.payload, &frozen); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || bodies[0] != bodies[1] || keys[0] != keys[1] || !strings.Contains(frozen.Text, "10") {
		t.Fatal("retry payload or key changed")
	}
	restarted := &Service{Config: s.Config, Store: st, Client: server.Client(), Endpoint: server.URL}
	if err := restarted.SendDue(context.Background(), cutoff.Add(time.Hour)); err != nil || calls != 2 {
		t.Fatal("restart resent report", err)
	}
}

func TestConfigDisabledByDefault(t *testing.T) {
	t.Setenv("SUBPOOL_DAILY_REPORT_ENABLED", "")
	c, err := LoadConfig("https://pool.example.com")
	if err != nil || c.Enabled {
		t.Fatal(c, err)
	}
}
