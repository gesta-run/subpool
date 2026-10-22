package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResetNoticeEventsAndClaim(t *testing.T) {
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
	aid, _ := id.New()
	digest := sha256.Sum256([]byte(aid))
	email := aid + "@example.com"
	a := domain.ProviderAccount{ID: aid, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative account", Email: email, SubjectHMAC: digest[:], CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
	if err = db.CreateProviderAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2040, 1, 2, 1, 0, 0, 0, time.UTC)
	end := start.Add(15 * time.Hour)
	quota := []byte(fmt.Sprintf(`{"weekly":{"remaining_percent":50,"reset_at":%d}}`, start.Add(time.Hour).Unix()))
	if err = db.UpdateProviderDetails(ctx, aid, email, quota, start); err != nil {
		t.Fatal(err)
	}
	if err = db.PlanReset(ctx, ResetJob{AccountID: aid, CreditID: "illustrative-credit", Label: email, RunAt: start.Add(12 * time.Hour), ExpiresAt: end.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	events, err := db.ResetNoticeEvents(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, e := range events {
		if e.Account == email {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("missing weekly or planned reset: %d", count)
	}
	// Past resets and stale weekly snapshots are excluded.
	if err = db.UpdateProviderDetails(ctx, aid, email, quota, start.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	events, err = db.ResetNoticeEvents(ctx, start.Add(13*time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Account == email {
			t.Fatal("stale or completed reset was included")
		}
	}
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, e := db.ClaimResetNotice(ctx, "2040-01-02", aid, "Illustrative message")
			if e != nil {
				t.Error(e)
			}
			if ok {
				claimed.Add(1)
			}
		}()
	}
	wg.Wait()
	if claimed.Load() != 1 {
		t.Fatal("notice was not claimed once")
	}
	if err = db.FinishResetNotice(ctx, "2040-01-02", aid, "sent", "https://example.com/receipt"); err != nil {
		t.Fatal(err)
	}
	if ok, e := db.ClaimResetNotice(ctx, "2040-01-02", aid, "Changed message"); e != nil || ok {
		t.Fatal("restart reclaimed completed notice")
	}
}
