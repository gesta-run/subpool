package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/reporting"
)

func TestSelectedResetAndRestartSafety(t *testing.T) {
	for _, scenario := range []string{"success", "uncertain", "uncertain_missing", "full", "missing", "expired", "redeemed"} {
		t.Run(scenario, func(t *testing.T) {
			posts, saved := 0, false
			expires := time.Now().Add(time.Hour).Unix()
			if scenario == "expired" {
				expires = time.Now().Add(-time.Minute).Unix()
			}
			remaining := 60.0
			if scenario == "full" {
				remaining = 100
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/v1/auth/login":
					http.SetCookie(w, &http.Cookie{Name: "session", Value: "example-session", Secure: true})
					w.Write([]byte(`{}`))
				case r.URL.Path == "/api/v1/provider-accounts":
					json.NewEncoder(w).Encode(map[string]any{"data": []account{{ID: "example-account", Email: "owner@example.test", Quota: &codex.UsageSnapshot{Weekly: &codex.UsageWindow{RemainingPercent: remaining}}}}})
				case r.URL.Path == "/api/v1/provider-accounts/example-account/reset-credits":
					selected := "selected-credit"
					status := "available"
					if scenario == "missing" || (scenario == "uncertain_missing" && posts > 0) {
						selected = "another-credit"
					}
					if scenario == "redeemed" || (scenario == "success" && posts > 0) {
						status = "redeemed"
					}
					json.NewEncoder(w).Encode(map[string]any{"reset_credits": codex.ResetCreditsSummary{AvailableCount: 1, Credits: []codex.ResetCredit{{ID: selected, Status: status, ExpiresAt: &expires}}}})
				case r.URL.Path == "/api/v1/provider-accounts/example-account/reset-credits/consume":
					if !saved {
						t.Error("redemption was attempted before durable state was saved")
					}
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					if body["credit_id"] != "selected-credit" {
						t.Error("selected a different credit")
					}
					posts++
					if scenario == "uncertain" || scenario == "uncertain_missing" {
						w.WriteHeader(502)
						return
					}
					remaining = 100
					json.NewEncoder(w).Encode(codex.ConsumeResetCreditResult{Outcome: "reset"})
				case r.URL.Path == "/api/v1/provider-accounts/example-account/check":
					w.Write([]byte(`{}`))
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			a := &api{origin: server.URL, client: server.Client()}
			j := job{AccountID: "example-account", CreditID: "selected-credit"}
			s := state{}
			save := func() error { saved = true; return nil }
			if err := execute(context.Background(), a, j, &s, save); err != nil {
				t.Fatal(err)
			}
			want := "failed"
			switch scenario {
			case "success":
				want = "succeeded"
			case "uncertain", "uncertain_missing":
				want = "unconfirmed"
			case "full":
				want = "skipped"
			}
			if s.Outcome != want {
				t.Fatalf("outcome=%s want=%s", s.Outcome, want)
			}
			wantPosts := 0
			if scenario == "success" || scenario == "uncertain" || scenario == "uncertain_missing" {
				wantPosts = 1
			}
			if posts != wantPosts {
				t.Fatalf("redemptions=%d want=%d", posts, wantPosts)
			}
			if err := execute(context.Background(), a, j, &s, save); err != nil {
				t.Fatal(err)
			}
			if posts != wantPosts {
				t.Fatalf("restart redeemed another credit: %d", posts)
			}
		})
	}
}

func TestUnavailableUsageStillProducesAnHonestNotification(t *testing.T) {
	t.Setenv("SUBPOOL_DATABASE_URL", "postgres://invalid:invalid@127.0.0.1:1/example?sslmode=disable&connect_timeout=1")
	c := reporting.Config{From: "SubPool <sender@example.test>", To: []string{"owner@example.test"}, Location: time.UTC, PublicURL: "https://example.test"}
	m, err := message(context.Background(), c, job{AccountID: "example", AccountLabel: "owner@example.test"}, state{Outcome: "unconfirmed", Detail: "Please check manually.", AttemptedAt: time.Now()}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.To) != 1 || m.To[0] != "owner@example.test" {
		t.Fatalf("unexpected recipients: %v", m.To)
	}
	if !strings.Contains(m.HTML, "Usage snapshot unavailable") || strings.Contains(m.HTML, "0 recorded calls") || !strings.Contains(m.Subject, "unconfirmed") {
		t.Fatalf("misleading fallback message: %s", m.Subject)
	}
}

func TestStateWriteFailurePreventsRedemption(t *testing.T) {
	s := state{}
	a := &api{origin: "http://127.0.0.1:1", client: &http.Client{Timeout: time.Second}}
	err := execute(context.Background(), a, job{}, &s, func() error { return context.Canceled })
	if err != context.Canceled {
		t.Fatalf("save error was lost: %v", err)
	}
}
