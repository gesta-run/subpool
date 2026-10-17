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
	account := domain.ProviderAccount{ID: accountID, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative report account", Email: "account@example.com", SubjectHMAC: digest[:], CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
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
			if a.Email != account.Email {
				t.Fatal("report account email was not loaded")
			}
		}
	}
	if count != 2 {
		t.Fatalf("duplicate response counted: %d", count)
	}
	baseline, err := db.ReportUnattributedRequests(ctx, start, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{"one", "two", "three"} {
		hash := sha256.Sum256([]byte("usage:" + response))
		if err = db.AddUsage(ctx, keyID, hash[:], "illustrative-model", time.Now(), 2, 1); err != nil {
			t.Fatal(err)
		}
	}
	gap, err := db.ReportUnattributedRequests(ctx, start, time.Now().Add(time.Second))
	if err != nil || gap != baseline+1 {
		t.Fatalf("missing-account count=%d baseline=%d error=%v", gap, baseline, err)
	}
	third := sha256.Sum256([]byte("three"))
	for range 2 {
		if err = db.RecordProviderRequest(ctx, keyID, third[:], accountID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	gap, err = db.ReportUnattributedRequests(ctx, start, time.Now().Add(time.Second))
	if err != nil || gap != baseline {
		t.Fatalf("recorded call remained unattributed: %d %v", gap, err)
	}
	accounts, err = db.ReportAccounts(ctx, start, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accounts {
		if a.ID == accountID && a.Requests != 3 {
			t.Fatalf("usage and response paths double-counted: %d", a.Requests)
		}
	}
	for _, provider := range []string{domain.ProviderCodex, domain.ProviderOpenAICompatible, domain.ProviderCopilot} {
		idleID, _ := id.New()
		digest := sha256.Sum256([]byte(idleID))
		idle := domain.ProviderAccount{ID: idleID, Provider: provider, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative idle account", SubjectHMAC: digest[:], CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
		if err = db.CreateProviderAccount(ctx, idle); err != nil {
			t.Fatal(err)
		}
		rows, e := db.ReportAccounts(ctx, start, time.Now().Add(time.Second))
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, row := range rows {
			if row.ID == idleID {
				found = true
				if row.Requests != 0 {
					t.Fatal("idle account has requests")
				}
			}
		}
		if !found {
			t.Fatal("provider account omitted from report", provider)
		}
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
