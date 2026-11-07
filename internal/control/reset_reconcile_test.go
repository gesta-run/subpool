package control

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

type discoveryJournal struct {
	autoResetStore
	reconciled bool
	available  []string
	planned    []store.ResetJob
}

func (j *discoveryJournal) ReconcilePendingResets(_ context.Context, _ string, ids []string) error {
	j.reconciled, j.available = true, ids
	return nil
}

func (j *discoveryJournal) PlanReset(_ context.Context, job store.ResetJob) error {
	j.planned = append(j.planned, job)
	return nil
}

type failedCreditRead struct{ autoTestCredits }

func (failedCreditRead) ReadResetCredits(context.Context, codex.Credentials) (*codex.ResetCreditsSummary, error) {
	return nil, errors.New("illustrative upstream outage")
}

func TestDiscoveryReconcilesOnlyVerifiedAvailableCredits(t *testing.T) {
	now := time.Now().UTC()
	future, past := now.Add(48*time.Hour).Unix(), now.Add(-time.Hour).Unix()
	zone, _ := time.LoadLocation("Asia/Shanghai")
	for _, scenario := range []string{"available", "empty", "refresh-failed", "missing-snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			s, st, cipher := newControlServer(t)
			raw, _ := json.Marshal(codex.Credentials{AccessToken: "illustrative", AccountID: "illustrative", ExpiresAt: now.Add(time.Hour)})
			encrypted, _ := cipher.Encrypt(raw)
			st.account = domain.ProviderAccount{ID: "example-account", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, CredentialCiphertext: encrypted, Status: domain.AccountActive}
			st.accounts = []domain.ProviderAccount{st.account}
			credits := codex.ResetCreditsSummary{Credits: []codex.ResetCredit{
				{ID: "future-credit", Status: "available", ExpiresAt: &future},
				{ID: "redeemed-credit", Status: "redeemed", ExpiresAt: &future},
				{ID: "expired-credit", Status: "available", ExpiresAt: &past},
				{ID: "unknown-expiry", Status: "available"},
			}}
			if scenario == "empty" {
				credits.Credits = nil
			}
			st.resetSnapshot, _ = json.Marshal(credits)
			st.resetCheckedAt = &now
			if scenario == "refresh-failed" || scenario == "missing-snapshot" {
				old := now.Add(-time.Hour)
				st.resetCheckedAt = &old
				s.resets = &failedCreditRead{}
			}
			if scenario == "missing-snapshot" {
				st.resetSnapshot = nil
			}
			journal := &discoveryJournal{}
			if err := s.discoverResets(context.Background(), journal, now, zone); err != nil {
				t.Fatal(err)
			}
			wantReconcile := scenario == "available" || scenario == "empty"
			if journal.reconciled != wantReconcile {
				t.Fatalf("reconciled=%v, want %v", journal.reconciled, wantReconcile)
			}
			if scenario == "available" && !reflect.DeepEqual(journal.available, []string{"future-credit"}) {
				t.Fatalf("unexpected available credits: %v", journal.available)
			}
			if scenario == "empty" && (len(journal.available) != 0 || len(journal.planned) != 0) {
				t.Fatal("empty authoritative list did not retire all obsolete pending jobs")
			}
			for _, job := range journal.planned {
				if job.CreditID != "future-credit" || job.RunAt != ResetTime(time.Unix(future, 0), zone) {
					t.Fatalf("changed expiry-based scheduling: %#v", job)
				}
			}
		})
	}
}
