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

	"github.com/gesta-run/subpool/internal/reporting/modelusage"
	"github.com/gesta-run/subpool/internal/reporting/people"
)

type fixedModelSource struct{ snapshot modelusage.Snapshot }

func (f fixedModelSource) Read(context.Context, time.Time, time.Time) (modelusage.Snapshot, error) {
	return f.snapshot, nil
}

func TestReportPeopleNamesAndExclusionsPreserveSourceAndRecomputeTotals(t *testing.T) {
	c := fixtureConfig()
	var err error
	c.People, err = people.Parse(`{"names":{"am":"Alex Morgan","alex@example.test":"Alex Morgan"},"excluded":["retired@example.test"]}`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	snapshot := modelusage.Snapshot{Employees: []modelusage.Employee{
		{ID: "a", Name: "am", Email: "alex@example.test", Models: []modelusage.Model{{Model: "gpt-6.1-sol", Turns: 8}}},
		{ID: "r", Name: "retired", Email: "retired@example.test", Models: []modelusage.Model{{Model: "excluded-model", Turns: 7}}},
	}}
	r := NewReport(c, []Account{{Name: "Account", Requests: 8, Employees: []Employee{{Name: "AM", Requests: 8}}}}, now.Add(-time.Hour), now, now, false, 0)
	s := &Service{Config: c, ModelUsage: fixedModelSource{snapshot}}
	s.fillModelUsage(context.Background(), &r, now.Add(-time.Hour), now)
	if r.Rows[0].Employees[0].Name != "Alex Morgan" || r.Rows[0].Employees[0].Requests != "8" || r.Rows[0].Employees[0].Share != "100.0%" {
		t.Fatal("account attribution changed")
	}
	if r.ModelUsage.Turns != "8" || r.ModelUsage.ModelCount != 1 || len(r.ModelUsage.Employees) != 1 || r.ModelUsage.Employees[0].Name != "Alex Morgan" {
		t.Fatalf("model summary: %+v", r.ModelUsage)
	}
	if snapshot.Employees[0].Name != "am" || len(snapshot.Employees) != 2 {
		t.Fatal("cached Gesta source was mutated")
	}
	m, err := Render(c, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{m.HTML, m.Text} {
		if !strings.Contains(body, "Alex Morgan") || strings.Contains(body, "retired") || strings.Contains(body, "excluded-model") {
			t.Fatal("report display policy not applied consistently")
		}
	}
}

func TestModelUsageEmailEscapesNamesKeepsEmptyEmployeesAndSeparatesMetrics(t *testing.T) {
	c := fixtureConfig()
	now := time.Now()
	r := NewReport(c, []Account{{Name: "Subscription", Requests: 5}}, now.Add(-time.Hour), now, now, false, 0)
	r.ModelUsage = modelusage.Build(modelusage.Snapshot{Employees: []modelusage.Employee{
		{ID: "alice", Name: "<script>alert(1)</script>", Models: []modelusage.Model{{Model: "gpt-6.1-sol", Turns: 8}, {Model: "unknown", Turns: 2}}},
		{ID: "empty", Name: "Empty employee"},
	}}, number)
	m, err := Render(c, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Account usage", "Models used by the team", "GPT-6.1 Sol", "80.0%", "Model not recorded", "Empty employee", "No turns recorded", "Turns and API requests are separate metrics"} {
		if !strings.Contains(m.HTML, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if strings.Contains(m.HTML, "<script>") || strings.Contains(m.HTML, "ZgotmplZ") || !strings.Contains(m.Text, "5 recorded calls") || !strings.Contains(m.Text, "10 recorded turns") {
		t.Fatal("escaping, styles, or metric separation failed")
	}
	r.ModelUsage = modelusage.View{Enabled: true, Unavailable: true}
	m, err = Render(c, r)
	if err != nil || !strings.Contains(m.HTML, "Employee model usage unavailable") || !strings.Contains(m.Text, "Account request counts above are unaffected") {
		t.Fatal("unavailable feed is not explicit", err)
	}
}

type failingModelSource struct{}

func (failingModelSource) Read(context.Context, time.Time, time.Time) (modelusage.Snapshot, error) {
	return modelusage.Snapshot{}, io.ErrUnexpectedEOF
}

func TestUnavailableModelFeedDoesNotBlockScheduledAccountEmail(t *testing.T) {
	var sent Message
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"id":"illustrative-email"}`)
	}))
	defer server.Close()
	s := &Service{Config: fixtureConfig(), Store: &fakeStore{count: 5}, ModelUsage: failingModelSource{}, Client: server.Client(), Endpoint: server.URL}
	cutoff := time.Date(2026, 1, 2, 9, 50, 0, 0, time.UTC)
	if err := s.SendDue(context.Background(), cutoff); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.HTML, "Employee model usage unavailable") || !strings.Contains(sent.Text, "5 recorded calls") {
		t.Fatal("account email was suppressed or data mixed")
	}
}

func fixtureConfig() Config {
	location, _ := time.LoadLocation("Asia/Shanghai")
	return Config{Enabled: true, APIKey: "test-key", From: "SubPool <reports@example.com>", To: []string{"team@example.com"}, PublicURL: "https://pool.example.com", Location: location, Hour: 17, Minute: 50}
}

func TestRenderTeamUsage(t *testing.T) {
	c := fixtureConfig()
	now := time.Date(2026, 1, 2, 9, 50, 0, 0, time.UTC)
	// All account counts and capacities in this fixture are illustrative.
	accounts := []Account{{Name: "Codex 1", Requests: 1280, Snapshot: []byte(`{"weekly":{"remaining_percent":72,"reset_at":2000000000}}`), CheckedAt: now}, {Name: "Codex 2", Requests: 960, Snapshot: []byte(`{"weekly":{"remaining_percent":64,"reset_at":2000000000}}`), CheckedAt: now}}
	report := NewReport(c, accounts, now.Add(-17*time.Hour-50*time.Minute), now, now, false, 0)
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
	stale := NewReport(c, accounts, now.Add(-time.Hour), now, now, false, 0)
	if stale.Rows[0].Remaining != "Unavailable" {
		t.Fatal("stale quota was reported as current")
	}
	accounts[0].CheckedAt = now
	accounts[0].Snapshot = []byte(`{"weekly":{"reset_at":2000000000}}`)
	missing := NewReport(c, accounts, now.Add(-time.Hour), now, now, false, 0)
	if missing.Rows[0].Remaining != "Unavailable" {
		t.Fatal("missing quota was shown as zero")
	}
	empty := NewReport(c, nil, now.Add(-time.Hour), now, now, false, 0)
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
	report := NewReport(c, accounts, now.Add(-17*time.Minute), now, now, true, 0)
	if report.Rows[0].Name != "account@example.com" {
		t.Fatal("account email was not used")
	}
	if report.Rows[0].Reset != "Weekly · Resets 2026-01-08 · 13:50 (in 6 days)" {
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

func TestGestaUsageConfigRequiresPairedHTTPFeed(t *testing.T) {
	for key, value := range map[string]string{
		"SUBPOOL_DAILY_REPORT_ENABLED": "true", "SUBPOOL_RESEND_API_KEY": "illustrative-key",
		"SUBPOOL_DAILY_REPORT_FROM": "SubPool <sender@example.test>", "SUBPOOL_DAILY_REPORT_TO": "team@example.test",
		"SUBPOOL_DAILY_REPORT_TIME": "17:00", "SUBPOOL_DAILY_REPORT_TIMEZONE": "Asia/Shanghai",
	} {
		t.Setenv(key, value)
	}
	for _, tc := range []struct {
		url, token string
		valid      bool
	}{
		{"", "", true}, {"https://gesta.example.test/api/v1/reports/employee-models", "illustrative-token", true},
		{"http://control:8080/api/v1/reports/employee-models", "illustrative-token", true},
		{"https://gesta.example.test", "", false}, {"", "illustrative-token", false},
		{"file:///private/feed", "illustrative-token", false}, {"https://user:pass@example.test", "illustrative-token", false},
	} {
		t.Setenv("SUBPOOL_GESTA_USAGE_REPORT_URL", tc.url)
		t.Setenv("SUBPOOL_GESTA_USAGE_REPORT_TOKEN", tc.token)
		c, err := LoadConfig("https://example.test")
		if (err == nil) != tc.valid {
			t.Fatalf("URL %q valid=%t: %v", tc.url, tc.valid, err)
		}
		if err == nil && (c.Hour != 17 || c.Minute != 0) {
			t.Fatal("existing schedule changed")
		}
	}
}

func (f *fakeStore) ReportUnattributedRequests(context.Context, time.Time, time.Time) (int64, error) {
	return 0, nil
}

func TestAllAccountQuotaKindsAndUnattributedFootnote(t *testing.T) {
	c := fixtureConfig()
	now := time.Date(2026, 1, 2, 9, 50, 0, 0, time.UTC)
	reset := now.Add(6 * 24 * time.Hour).Unix()
	snapshot := func(kind string, values map[string]any) []byte {
		values["reset_at"] = reset
		raw, _ := json.Marshal(map[string]any{kind: values})
		return raw
	}
	// All accounts, counts, and capacities below are illustrative.
	accounts := []Account{
		{Email: "first@example.com", Requests: 10, CheckedAt: now, Snapshot: snapshot("weekly", map[string]any{"remaining_percent": 72})},
		{Email: "second@example.com", Requests: 20, CheckedAt: now, Snapshot: snapshot("weekly", map[string]any{"remaining_percent": 64})},
		{Name: "Example model API", Requests: 8, CheckedAt: now, Snapshot: snapshot("monthly", map[string]any{"remaining_percent": 58.5})},
		{Name: "Example coding assistant", Requests: 1, CheckedAt: now, Snapshot: snapshot("credits", map[string]any{"remaining_percent": 75, "remaining": 900, "entitlement": 1200})},
	}
	r := NewReport(c, accounts, now.Add(-time.Hour), now, now, false, 1)
	if r.Total != 40 || len(r.Rows) != 4 || r.Rows[0].Share != "25.0%" {
		t.Fatal(r)
	}
	for i, want := range []string{"72%", "64%", "58.5%", "900 / 1,200"} {
		if r.Rows[i].Remaining != want {
			t.Fatalf("row %d: %s", i, r.Rows[i].Remaining)
		}
	}
	for i, want := range []string{"Weekly ·", "Weekly ·", "Monthly ·", "Credits ·"} {
		if !strings.HasPrefix(r.Rows[i].Reset, want) {
			t.Fatal(r.Rows[i].Reset)
		}
	}
	message, err := Render(c, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{message.HTML, message.Text} {
		for _, want := range []string{"first@example.com", "second@example.com", "Example model API", "Example coding assistant", "Calls without retained account attribution: 1 (2.5%)", "40 recorded calls"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q", want)
			}
		}
		if strings.Contains(body, "Account not recorded") {
			t.Fatal("unattributed calls rendered as an account")
		}
	}
	accounts[2].CheckedAt = now.Add(-16 * time.Minute)
	r = NewReport(c, accounts, now.Add(-time.Hour), now, now, false, 0)
	if r.Rows[2].Remaining != "Unavailable" {
		t.Fatal("stale monthly quota was shown")
	}
}

func TestEmployeeSharesUseAccountTotalAndEscapeNames(t *testing.T) {
	c := fixtureConfig()
	now := time.Date(2026, 2, 3, 9, 0, 0, 0, time.UTC)
	// All account identities, contributor names and request counts are illustrative.
	accounts := []Account{
		{Name: "Example account", Requests: 12, Employees: []Employee{{Name: "Example <A>", Requests: 8}, {Name: "Example B", Requests: 4}}},
		{Name: "Other account", Requests: 18},
		{Name: "Idle account", Employees: []Employee{{Name: "Example C"}}},
	}
	r := NewReport(c, accounts, now.Add(-time.Hour), now, now, false, 0)
	if r.Rows[0].Share != "40.0%" || r.Rows[0].Employees[0].Share != "66.7%" || r.Rows[0].Employees[1].Share != "33.3%" {
		t.Fatalf("employee share must use its account total: %+v", r.Rows[0])
	}
	if r.Rows[2].Employees[0].Share != "0.0%" {
		t.Fatal("zero-count account produced an invalid percentage")
	}
	m, err := Render(c, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{m.HTML, m.Text} {
		for _, want := range []string{"8 · 66.7%", "4 · 33.3%", "Employee percentages are within each account"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q", want)
			}
		}
	}
	if !strings.Contains(m.HTML, "Employee requests") || !strings.Contains(m.HTML, "Example &lt;A&gt;") || strings.Contains(m.HTML, "Example <A>") {
		t.Fatal("employee column missing or employee name was not escaped")
	}
}
