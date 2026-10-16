package store

import (
	"context"
	"crypto/sha256"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
)

func TestReportCountsPersistDeduplicateAndClaimOnce(t *testing.T) {
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
	accountID, _ := id.New()
	poolID, _ := id.New()
	keyID, _ := id.New()
	employeeID, _ := id.New()
	digest := sha256.Sum256([]byte(accountID))
	account := domain.ProviderAccount{ID: accountID, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative report account", SubjectHMAC: digest[:], CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
	if err = db.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: poolID, Name: "Report test " + poolID, Provider: domain.ProviderCodex, Accounts: []domain.PoolAccount{{PoolID: poolID, ProviderAccountID: accountID, Weight: 1, Enabled: true}}}
	if err = db.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	key := domain.APIKey{ID: keyID, EmployeeID: employeeID, EmployeeName: "Illustrative employee", PoolID: poolID, KeyHMAC: digest[:], KeyHint: "test", Scopes: []string{"responses"}}
	if _, err = db.CreateAPIKeyAndBind(ctx, key); err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Second)
	for _, response := range []string{"one", "one", "two"} {
		h := sha256.Sum256([]byte(response))
		if err = db.SaveSessionBinding(ctx, keyID, poolID, h[:], accountID, time.Now().Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	accounts, err := db.ReportAccounts(ctx, start, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	for _, a := range accounts {
		if a.ID == accountID {
			count = a.Requests
		}
	}
	if count != 2 {
		t.Fatalf("duplicate response counted: %d", count)
	}
	day := "2099-01-02"
	var won atomic.Int64
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, claimed, e := db.ClaimDailyReport(ctx, day, []byte(`{"subject":"illustrative"}`))
			if e != nil {
				t.Error(e)
			}
			if claimed {
				won.Add(1)
			}
		}()
	}
	workers.Wait()
	if won.Load() != 1 {
		t.Fatalf("concurrent claims: %d", won.Load())
	}
	if err = db.FinishDailyReport(ctx, day, "illustrative-email-id"); err != nil {
		t.Fatal(err)
	}
	if _, claimed, e := db.ClaimDailyReport(ctx, day, []byte(`{}`)); e != nil || claimed {
		t.Fatal("sent report was claimed again", e)
	}
}
