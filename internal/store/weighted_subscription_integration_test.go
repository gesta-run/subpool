package store

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
)

func TestPostgresWeightedSubscriptionsPreserveBindingsAndFailOver(t *testing.T) {
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
	primary := "00000000-0000-4000-8000-000000009731"
	secondary := "00000000-0000-4000-8000-000000009732"
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000009733", Name: "Illustrative weighted pool", Provider: domain.ProviderCodex}
	if err = db.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	db.WithCodexQuotaRouting(true).WithCodexRequestWeights(map[string]uint64{primary: 4, secondary: 1})
	now := time.Now()
	setQuota := func(id string, used float64, credits string) {
		t.Helper()
		if err := db.UpdateProviderDetails(ctx, id, "", testCreditQuota(now, used, credits), now); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range []string{primary, secondary} {
		account := domain.ProviderAccount{
			ID: id, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription,
			DisplayName: "Illustrative subscription", Status: domain.AccountActive, HealthStatus: domain.HealthHealthy,
			SubjectHMAC: bytes.Repeat([]byte{byte(97 + i)}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1,
		}
		if err = db.CreateProviderAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
		if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: id, Weight: 1, Priority: i, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		setQuota(id, float64(60-i*20), "100")
	}
	key := domain.APIKey{ID: "00000000-0000-4000-8000-000000009734", PoolID: pool.ID, EmployeeName: "Illustrative employee", KeyHMAC: bytes.Repeat([]byte{99}, 32), KeyHint: "test"}
	initial, err := db.CreateAPIKeyAndBind(ctx, key)
	if err != nil || initial != primary {
		t.Fatalf("default account = %s, err = %v", initial, err)
	}
	hash := bytes.Repeat([]byte{100}, 32)
	if err = db.SaveSessionBinding(ctx, key.ID, pool.ID, hash, primary, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	route, err := db.ResolveAPIKey(ctx, key.KeyHMAC)
	if err != nil {
		t.Fatal(err)
	}
	choose := func() string {
		t.Helper()
		account, err := db.PreferQuotaAccount(ctx, route)
		if err != nil {
			t.Fatal(err)
		}
		return account.ID
	}
	counts := make(map[string]int)
	for range 10 {
		counts[choose()]++
	}
	if counts[primary] != 8 || counts[secondary] != 2 {
		t.Fatalf("weighted distribution = %v", counts)
	}
	if bound, err := db.ResolveAPIKey(ctx, key.KeyHMAC); err != nil || bound.Account.ID != primary {
		t.Fatalf("default account changed to %s: %v", bound.Account.ID, err)
	}
	if pinned, err := db.ResolveSessionAccount(ctx, key.ID, hash); err != nil || pinned.ID != primary {
		t.Fatalf("existing session moved to %s: %v", pinned.ID, err)
	}
	setQuota(primary, 100, "100")
	for range 5 {
		if got := choose(); got != secondary {
			t.Fatalf("expected remaining subscription, got %s", got)
		}
	}
	setQuota(primary, 60, "100")
	setQuota(secondary, 100, "100")
	for range 5 {
		if got := choose(); got != primary {
			t.Fatalf("expected remaining subscription, got %s", got)
		}
	}
}
