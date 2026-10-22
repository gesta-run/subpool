package resetnotice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type testStore struct {
	events           []Event
	claimed          bool
	reads, claims    int
	outcome, message string
	start, end       time.Time
}

func (s *testStore) ResetNoticeEvents(_ context.Context, start, end time.Time) ([]Event, error) {
	s.reads++
	s.start, s.end = start, end
	return s.events, nil
}
func (s *testStore) ClaimResetNotice(_ context.Context, day, channel, message string) (bool, error) {
	s.claims++
	if s.claimed {
		return false, nil
	}
	s.claimed = true
	s.message = message
	return true, nil
}
func (s *testStore) FinishResetNotice(_ context.Context, day, channel, outcome, link string) error {
	s.outcome = outcome
	return nil
}

type testSender struct {
	calls int
	err   error
}

func (s *testSender) Send(context.Context, string, string) (string, error) {
	s.calls++
	return "https://example.com/receipt", s.err
}

func TestNoticeWindowAndDurableDeduplication(t *testing.T) {
	now := time.Date(2026, 1, 6, 1, 0, 12, 0, time.UTC) // Tuesday, 09:00 Beijing.
	st := &testStore{events: []Event{{Account: "first@example.com", Kind: "weekly reset", At: now.Add(5 * time.Hour)}, {Account: "second@example.com", Kind: "scheduled reset credit", At: now.Add(12 * time.Hour)}}}
	sender := &testSender{}
	s := &Service{Config: Config{Enabled: true, Channel: "illustrative-channel"}, Store: st, Sender: sender}
	for _, at := range []time.Time{now.Add(-time.Minute), now.Add(time.Minute), now.Add(4 * 24 * time.Hour), now.Add(5 * 24 * time.Hour)} {
		if err := s.SendDue(context.Background(), at); err != nil {
			t.Fatal(err)
		}
	}
	if st.reads != 0 || sender.calls != 0 {
		t.Fatal("queried or sent outside weekday 09:00 minute")
	}
	if err := s.SendDue(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{Config: s.Config, Store: st, Sender: sender}
	if err := restarted.SendDue(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 1 || st.outcome != "sent" || !st.end.Equal(time.Date(2026, 1, 6, 16, 0, 0, 0, time.UTC)) {
		t.Fatal("wrong schedule, duplicate delivery, or day boundary")
	}
	for _, want := range []string{"<!here>", "January 6, 2026", "Beijing time, UTC+8", "**14:00**", "**21:00**", "first@example.com", "weekly reset", "scheduled reset credit"} {
		if !strings.Contains(st.message, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestNoResetAndUncertainDelivery(t *testing.T) {
	now := time.Date(2026, 1, 6, 1, 0, 0, 0, time.UTC)
	st := &testStore{}
	sender := &testSender{err: errors.New("timeout")}
	s := &Service{Config: Config{Enabled: true, Channel: "illustrative-channel"}, Store: st, Sender: sender}
	if err := s.SendDue(context.Background(), now); err != nil || st.claims != 0 {
		t.Fatal("empty day claimed a message")
	}
	st.events = []Event{{Account: "account@example.com", Kind: "weekly reset", At: now.Add(time.Hour)}}
	if err := s.SendDue(context.Background(), now); err == nil || st.outcome != "unconfirmed" {
		t.Fatal("uncertain delivery lost its state")
	}
	if err := s.SendDue(context.Background(), now.Add(time.Second)); err != nil || sender.calls != 1 {
		t.Fatal("uncertain message was reposted")
	}
}

func TestConfigOptIn(t *testing.T) {
	t.Setenv("SUBPOOL_RESET_NOTICE_ENABLED", "")
	c, err := LoadConfig()
	if err != nil || c.Enabled {
		t.Fatal(c, err)
	}
	t.Setenv("SUBPOOL_RESET_NOTICE_ENABLED", "true")
	if _, err = LoadConfig(); err == nil {
		t.Fatal("enabled without delivery configuration")
	}
	t.Setenv("SUBPOOL_RESET_NOTICE_MCP_URL", "https://example.com/mcp")
	t.Setenv("SUBPOOL_RESET_NOTICE_MCP_AUTHORIZATION", "Bearer illustrative")
	t.Setenv("SUBPOOL_RESET_NOTICE_CHANNEL", "illustrative-channel")
	if _, err = LoadConfig(); err != nil {
		t.Fatal(err)
	}
}
