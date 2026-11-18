package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
)

func TestPostgresPromotionalGrantsBetweenSubscriptionAndCredits(t *testing.T) {
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
	db.WithCodexQuotaRouting(true)
	now := time.Now()
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000009710", Name: "Illustrative grant pool", Provider: domain.ProviderCodex}
	if err = db.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 4)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-4000-8000-%012d", 9700+i)
		account := domain.ProviderAccount{ID: ids[i], Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative account", SubjectHMAC: bytes.Repeat([]byte{byte(81 + i)}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
		if i >= 2 {
			account.Provider = domain.ProviderOpenAICompatible
			account.CredentialType = domain.CredentialAPIKey
		}
		if err = db.CreateProviderAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: ids[i], Weight: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	set := func(i int, raw []byte) {
		t.Helper()
		if e := db.UpdateProviderDetails(ctx, ids[i], "", raw, now); e != nil {
			t.Fatal(e)
		}
	}
	set(0, testCreditQuota(now, 100, "400"))
	set(1, testCreditQuota(now, 100, "800"))
	set(2, testPromotionalQuota(now, 5, time.Hour))
	set(3, testPromotionalQuota(now, 40, 2*time.Hour))
	key := domain.APIKey{ID: "00000000-0000-4000-8000-000000009711", PoolID: pool.ID, EmployeeName: "Illustrative grant employee", KeyHMAC: bytes.Repeat([]byte{85}, 32), KeyHint: "test"}
	initial, err := db.CreateAPIKeyAndBind(ctx, key)
	if err != nil || initial != ids[2] {
		t.Fatalf("initial=%s err=%v", initial, err)
	}
	assert := func(want int, exclude []string) {
		t.Helper()
		a, e := db.ReassignAPIKey(ctx, key.ID, pool.ID, exclude)
		if e != nil || a.ID != ids[want] {
			t.Fatalf("got=%s want=%s err=%v", a.ID, ids[want], e)
		}
	}
	assert(3, []string{ids[2]})
	set(2, testPromotionalQuota(now, 0, time.Hour))
	assert(3, nil)
	set(3, testPromotionalQuota(now, 40, -time.Second))
	assert(1, nil)
	set(0, testCreditQuota(now, 50, "400"))
	assert(0, nil)
	// A restored subscription takes precedence over an existing API binding too.
	set(2, testPromotionalQuota(now, 5, time.Hour))
	set(0, testCreditQuota(now, 100, "400"))
	assert(2, nil)
	set(0, testCreditQuota(now, 50, "400"))
	route, e := db.ResolveAPIKey(ctx, key.KeyHMAC)
	if e != nil {
		t.Fatal(e)
	}
	a, e := db.PreferQuotaAccount(ctx, route)
	if e != nil || a.ID != ids[0] {
		t.Fatalf("recovery=%s err=%v", a.ID, e)
	}
	// Ordinary API-only pools retain the existing allocator and do not acquire
	// subscription or grant restrictions from the opt-in Codex policy.
	apiPool := domain.Pool{ID: "00000000-0000-4000-8000-000000009712", Name: "Illustrative API-only pool", Provider: domain.ProviderOpenAICompatible}
	if e = db.CreatePool(ctx, apiPool); e != nil {
		t.Fatal(e)
	}
	if e = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: apiPool.ID, ProviderAccountID: ids[3], Weight: 1, Enabled: true}); e != nil {
		t.Fatal(e)
	}
	key.ID = "00000000-0000-4000-8000-000000009713"
	key.PoolID = apiPool.ID
	key.KeyHMAC = bytes.Repeat([]byte{86}, 32)
	if id, e := db.CreateAPIKeyAndBind(ctx, key); e != nil || id != ids[3] {
		t.Fatalf("ordinary API changed: %s %v", id, e)
	}
}
