package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/reporting"
	"github.com/gesta-run/subpool/internal/store"
)

type eventSnapshot struct{ reporting.Store }

func (eventSnapshot) ReportTrackingStarted(context.Context) (time.Time, error) {
	return time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), nil
}
func (eventSnapshot) ReportAccounts(context.Context, time.Time, time.Time) ([]reporting.Account, error) {
	return []reporting.Account{{Email: "first@example.test", Requests: 12}, {Email: "second@example.test", Requests: 8}}, nil
}
func (eventSnapshot) ReportUnattributedRequests(context.Context, time.Time, time.Time) (int64, error) {
	return 0, nil
}

type reportJournal struct {
	autoResetStore
	batch *store.ResetBatch
	done  bool
}

func (j *reportJournal) ClaimResetBatch(context.Context, time.Time, bool) (*store.ResetBatch, error) {
	if j.done {
		return nil, nil
	}
	return j.batch, nil
}
func (j *reportJournal) FreezeResetReport(_ context.Context, _ string, payload []byte) ([]byte, error) {
	if len(j.batch.Payload) == 0 {
		j.batch.Payload = payload
	}
	return j.batch.Payload, nil
}
func (j *reportJournal) FinishResetReport(context.Context, string, string) error {
	j.done = true
	return nil
}
func (j *reportJournal) RetryResetBatch(context.Context, string) error { return nil }

func TestBatchReportCombinesResultsAndRetriesOnlyMail(t *testing.T) {
	journal := &reportJournal{batch: &store.ResetBatch{ID: "illustrative-batch", Jobs: []store.ResetJob{
		{Label: "first@example.test", Outcome: "succeeded", Detail: "Illustrative success"},
		{Label: "second@example.test", Outcome: "failed", Detail: "Illustrative failure"},
	}}}
	var bodies, keys []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		if len(bodies) == 1 {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"illustrative-email"}`))
	}))
	defer endpoint.Close()
	zone, _ := time.LoadLocation("Asia/Shanghai")
	reporter := &reporting.Service{Config: reporting.Config{Enabled: true, APIKey: "illustrative", From: "SubPool <sender@example.test>", To: []string{"team@example.test"}, Location: zone, PublicURL: "https://example.test"}, Store: eventSnapshot{}, Endpoint: endpoint.URL, Client: endpoint.Client()}
	s := &Server{}
	if err := s.processResetBatch(context.Background(), journal, reporter, "owner@example.test", time.Now(), true); err == nil {
		t.Fatal("mail failure was swallowed")
	}
	// All outcomes are already recorded; retrying never calls a reset provider.
	if err := s.processResetBatch(context.Background(), journal, reporter, "owner@example.test", time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || keys[0] != keys[1] || !journal.done {
		t.Fatal("mail retry changed the frozen report or key")
	}
	var m reporting.Message
	if err := json.Unmarshal([]byte(bodies[1]), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.To) != 1 || m.To[0] != "owner@example.test" {
		t.Fatalf("wrong recipients %v", m.To)
	}
	for _, value := range []string{"first@example.test", "second@example.test", "succeeded", "failed", "Team usage overview", "EMPLOYEE REQUESTS"} {
		if !strings.Contains(m.HTML, value) {
			t.Fatalf("missing %s", value)
		}
	}
	if reporter.Config.To[0] != "team@example.test" {
		t.Fatal("changed daily report recipients")
	}
}
