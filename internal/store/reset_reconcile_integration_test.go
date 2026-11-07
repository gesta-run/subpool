package store

import (
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
)

func TestReconcilePendingResetsPreservesAvailableAndClaimedJobs(t *testing.T) {
	url := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, _ := id.New()
	h := sha256.Sum256([]byte(a))
	if err := db.CreateProviderAccount(ctx, domain.ProviderAccount{ID: a, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative account", SubjectHMAC: h[:], CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}); err != nil {
		t.Fatal(err)
	}
	defer db.DeleteProviderAccount(ctx, a)
	now := time.Now()
	for _, credit := range []string{"withdrawn", "available", "attempted", "claimed", "finished"} {
		job := ResetJob{AccountID: a, CreditID: credit, Label: "example@example.test", RunAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * time.Hour)}
		if credit == "claimed" {
			job.RunAt = now.Add(-time.Minute)
		}
		if err := db.PlanReset(ctx, job); err != nil {
			t.Fatal(err)
		}
		if credit == "attempted" {
			if err := db.StartResetAttempt(ctx, job); err != nil {
				t.Fatal(err)
			}
		}
		if credit == "finished" {
			job.Outcome = "succeeded"
			if err := db.FinishResetJob(ctx, job); err != nil {
				t.Fatal(err)
			}
		}
	}
	batch, err := db.ClaimResetBatch(ctx, now, true)
	if err != nil || batch == nil || len(batch.Jobs) != 1 {
		t.Fatalf("claim: %#v, %v", batch, err)
	}
	defer db.pool.Exec(ctx, `DELETE FROM codex_reset_batches WHERE id=$1`, batch.ID)
	defer db.pool.Exec(ctx, `DELETE FROM codex_reset_jobs WHERE account_id=$1`, a)
	if err := db.ReconcilePendingResets(ctx, a, []string{"available"}); err != nil {
		t.Fatal(err)
	}
	for credit, want := range map[string]string{"withdrawn": "skipped", "available": "", "attempted": "", "claimed": "", "finished": "succeeded"} {
		var outcome string
		if err := db.pool.QueryRow(ctx, `SELECT COALESCE(outcome,'') FROM codex_reset_jobs WHERE account_id=$1 AND credit_id=$2`, a, credit).Scan(&outcome); err != nil || outcome != want {
			t.Fatalf("%s outcome=%q want=%q err=%v", credit, outcome, want, err)
		}
	}
	if err := db.ReconcilePendingResets(ctx, a, []string{}); err != nil {
		t.Fatal(err)
	}
	var outcome string
	if err := db.pool.QueryRow(ctx, `SELECT outcome FROM codex_reset_jobs WHERE account_id=$1 AND credit_id='available'`, a).Scan(&outcome); err != nil || outcome != "skipped" {
		t.Fatalf("empty list: outcome=%q err=%v", outcome, err)
	}
}
