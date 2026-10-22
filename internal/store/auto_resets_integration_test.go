package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
)

func TestAutoResetBatchDurabilityAndFrozenReport(t *testing.T) {
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
	var accounts []string
	var batchID string
	defer func() {
		for _, a := range accounts {
			_, _ = db.pool.Exec(ctx, `DELETE FROM codex_reset_jobs WHERE account_id=$1`, a)
			_ = db.DeleteProviderAccount(ctx, a)
		}
		if batchID != "" {
			_, _ = db.pool.Exec(ctx, `DELETE FROM codex_reset_batches WHERE id=$1`, batchID)
		}
	}()
	now := time.Now()
	for range 2 {
		a, _ := id.New()
		accounts = append(accounts, a)
		h := sha256.Sum256([]byte(a))
		err := db.CreateProviderAccount(ctx, domain.ProviderAccount{ID: a, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative reset account", Email: "example@example.test", SubjectHMAC: h[:], CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.PlanReset(ctx, ResetJob{AccountID: a, CreditID: "illustrative-credit", Label: "example@example.test", RunAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if b, err := db.ClaimResetBatch(ctx, now, false); err != nil || b != nil {
		t.Fatalf("created a new batch outside window: %v %v", b, err)
	}
	b, err := db.ClaimResetBatch(ctx, now, true)
	if err != nil || b == nil || len(b.Jobs) != 2 {
		t.Fatalf("batch=%v err=%v", b, err)
	}
	batchID = b.ID
	if other, err := db.ClaimResetBatch(ctx, now, true); err != nil || other != nil {
		t.Fatalf("claimed a leased batch twice: %v %v", other, err)
	}
	b.Jobs[0].Before = []byte(`{"weekly":{"remaining_percent":50}}`)
	if err := db.StartResetAttempt(ctx, b.Jobs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE codex_reset_batches SET lease_until=now()-interval '1 minute' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	recovered, err := db.ClaimResetBatch(ctx, now, false)
	if err != nil || recovered == nil || recovered.ID != b.ID || !recovered.Jobs[0].AttemptedAt.Valid {
		t.Fatalf("lost reset intent: %#v %v", recovered, err)
	}
	for _, j := range recovered.Jobs {
		j.Outcome = "unconfirmed"
		j.Detail = "Illustrative recovery result"
		if err := db.FinishResetJob(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.FreezeResetReport(ctx, b.ID, []byte(`{"subject":"original"}`)); err != nil {
		t.Fatal(err)
	}
	frozen, err := db.FreezeResetReport(ctx, b.ID, []byte(`{"subject":"changed"}`))
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]string
	json.Unmarshal(frozen, &message)
	if message["subject"] != "original" {
		t.Fatal("changed an already frozen email")
	}
	if err := db.FinishResetReport(ctx, b.ID, "illustrative-email-id"); err != nil {
		t.Fatal(err)
	}
	if err := db.RetryResetBatch(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if again, err := db.ClaimResetBatch(ctx, now, true); err != nil || again != nil {
		t.Fatalf("reclaimed a completed batch: %v %v", again, err)
	}
}
