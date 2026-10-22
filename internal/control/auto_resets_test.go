package control

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

type resetTestJournal struct {
	autoResetStore
	mu       sync.Mutex
	started  map[string]bool
	startErr error
	finished []store.ResetJob
}

func (j *resetTestJournal) StartResetAttempt(_ context.Context, r store.ResetJob) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.startErr != nil {
		return j.startErr
	}
	if j.started == nil {
		j.started = map[string]bool{}
	}
	j.started[r.CreditID] = true
	return nil
}
func (j *resetTestJournal) FinishResetJob(_ context.Context, r store.ResetJob) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.finished = append(j.finished, r)
	return nil
}

type autoTestCredits struct {
	credits *codex.ResetCreditsSummary
	journal *resetTestJournal
	calls   int
	fail    bool
	t       *testing.T
}

func (c *autoTestCredits) ReadResetCredits(context.Context, codex.Credentials) (*codex.ResetCreditsSummary, error) {
	return c.credits, nil
}
func (c *autoTestCredits) ConsumeResetCredit(_ context.Context, _ codex.Credentials, credit, key string) (codex.ConsumeResetCreditResult, error) {
	c.journal.mu.Lock()
	defer c.journal.mu.Unlock()
	if !c.journal.started[credit] {
		c.t.Error("consumption preceded durable intent")
	}
	c.calls++
	if c.fail {
		return codex.ConsumeResetCreditResult{}, errors.New("ambiguous transport error")
	}
	return codex.ConsumeResetCreditResult{Outcome: "reset"}, nil
}

func TestAutoResetUncertaintyAndRecoveryNeverResubmit(t *testing.T) {
	for _, scenario := range []string{"success", "uncertain", "recovery", "missing", "full", "journal-failure", "window-closed"} {
		t.Run(scenario, func(t *testing.T) {
			s, st, cipher := newControlServer(t)
			raw, _ := json.Marshal(codex.Credentials{AccessToken: "illustrative", AccountID: "illustrative", ExpiresAt: time.Now().Add(time.Hour)})
			encrypted, _ := cipher.Encrypt(raw)
			st.account = domain.ProviderAccount{ID: "example-account", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, CredentialCiphertext: encrypted, Status: domain.AccountActive, QuotaSnapshot: []byte(`{"weekly":{"remaining_percent":50}}`)}
			if scenario == "full" {
				st.account.QuotaSnapshot = []byte(`{"weekly":{"remaining_percent":100}}`)
			}
			journal := &resetTestJournal{}
			if scenario == "journal-failure" {
				journal.startErr = context.Canceled
			}
			expires := time.Now().Add(time.Hour).Unix()
			resets := &autoTestCredits{credits: &codex.ResetCreditsSummary{Credits: []codex.ResetCredit{{ID: "example-credit", Status: "available", ExpiresAt: &expires}}}, journal: journal, t: t, fail: scenario == "uncertain"}
			if scenario == "missing" {
				resets.credits.Credits = nil
			}
			s.resets = resets
			job := store.ResetJob{AccountID: "example-account", CreditID: "example-credit"}
			if scenario == "recovery" {
				job.AttemptedAt.Valid = true
			}
			err := s.runResetJob(context.Background(), journal, &job, scenario != "window-closed")
			if scenario == "journal-failure" {
				if !errors.Is(err, context.Canceled) || resets.calls != 0 {
					t.Fatal("consumed despite failed intent persistence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, calls := "failed", 0
			switch scenario {
			case "success":
				want, calls = "succeeded", 1
			case "uncertain":
				want, calls = "unconfirmed", 1
			case "recovery":
				want = "unconfirmed"
			case "full":
				want = "skipped"
			}
			if job.Outcome != want || resets.calls != calls {
				t.Fatalf("outcome=%s calls=%d", job.Outcome, resets.calls)
			}
		})
	}
}
