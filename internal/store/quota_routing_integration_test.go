package store

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
)

func TestPostgresQuotaRoutingPreservesSessionsAndCapacity(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	settings, err := db.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.UpdateSettings(ctx, settings) }()
	setupSettings := settings
	setupSettings.MaxAPIKeysPerAccount = 10
	if err = db.UpdateSettings(ctx, setupSettings); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	accounts := make([]domain.ProviderAccount, 3)
	for i := range accounts {
		accounts[i] = domain.ProviderAccount{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 9100+i), Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative quota account", SubjectHMAC: bytes.Repeat([]byte{byte(191 + i)}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
		if err = db.CreateProviderAccount(ctx, accounts[i]); err != nil {
			t.Fatal(err)
		}
		if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testQuota(now, 10, 60-float64(i)*20), now); err != nil {
			t.Fatal(err)
		}
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000009110", Name: "Illustrative quota pool", Provider: domain.ProviderCodex}
	if err = db.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: accounts[0].ID, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	routes := make([]domain.KeyRoute, 3)
	for i := range routes {
		key := domain.APIKey{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 9120+i), PoolID: pool.ID, EmployeeName: "Illustrative employee", KeyHMAC: bytes.Repeat([]byte{byte(211 + i)}, 32), KeyHint: "test"}
		if _, err = db.CreateAPIKeyAndBind(ctx, key); err != nil {
			t.Fatal(err)
		}
		routes[i], err = db.ResolveAPIKey(ctx, key.KeyHMAC)
		routes[i].Key.KeyHMAC = key.KeyHMAC
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(accounts); i++ {
		// The highest-quota account has a different priority and must not be used.
		if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: accounts[i].ID, Weight: 1, Priority: i - 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	assertSame := func(label string) {
		t.Helper()
		a, e := db.PreferQuotaAccount(ctx, routes[0])
		if e != nil || a.ID != accounts[0].ID {
			t.Fatalf("%s: account=%s err=%v", label, a.ID, e)
		}
	}
	assertSame("disabled by default")
	db.WithCodexQuotaRouting(true)
	if err = db.UpdateProviderDetails(ctx, accounts[1].ID, "", testQuota(now, 10, 55), now); err != nil {
		t.Fatal(err)
	}
	assertSame("five-point hysteresis")
	if err = db.UpdateProviderDetails(ctx, accounts[1].ID, "", testQuota(now, 10, 40), now.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertSame("stale candidate")
	if err = db.UpdateProviderDetails(ctx, accounts[1].ID, "", testQuota(now, 10, 40), now); err != nil {
		t.Fatal(err)
	}
	if err = db.UpdateProviderDetails(ctx, accounts[0].ID, "", testQuota(now, 10, 60), now.Add(-20*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertSame("stale current")
	if err = db.UpdateProviderDetails(ctx, accounts[0].ID, "", testQuota(now, 10, 60), now); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{domain.AccountDisabled, domain.AccountExhausted, domain.AccountCoolingDown} {
		until := now.Add(time.Hour)
		if err = db.UpdateProviderStatus(ctx, accounts[1].ID, status, &until); err != nil {
			t.Fatal(err)
		}
		assertSame(status)
	}
	if err = db.UpdateProviderStatus(ctx, accounts[1].ID, domain.AccountActive, nil); err != nil {
		t.Fatal(err)
	}
	if err = db.SetProviderHealth(ctx, accounts[1].ID, domain.HealthUnhealthy, "test", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertSame("unhealthy candidate")
	if err = db.SetProviderHealth(ctx, accounts[1].ID, domain.HealthHealthy, "", now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	if err = db.UpdateProviderDetails(ctx, accounts[0].ID, "", testQuota(now, 10, 60), now); err != nil {
		t.Fatal(err)
	}
	// Weekly-only snapshots must still support concurrent, capacity-limited
	// reassignment while existing response bindings stay pinned.
	for i, used := range []float64{60, 40} {
		if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testQuotaWithWindow(now, "weekly", used), now); err != nil {
			t.Fatal(err)
		}
	}
	sessionHash := bytes.Repeat([]byte{220}, 32)
	if err = db.SaveSessionBinding(ctx, routes[0].Key.ID, pool.ID, sessionHash, accounts[0].ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	setupSettings.MaxAPIKeysPerAccount = 1
	if err = db.UpdateSettings(ctx, setupSettings); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for _, route := range routes {
		wg.Add(1)
		go func(route domain.KeyRoute) { defer wg.Done(); _, e := db.PreferQuotaAccount(ctx, route); results <- e }(route)
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			t.Fatal(e)
		}
	}
	if db.QuotaRoutingSwitches() != 1 {
		t.Fatalf("switches=%d, want one capacity-limited switch", db.QuotaRoutingSwitches())
	}
	counts := map[string]int{}
	var movedRoute domain.KeyRoute
	for _, route := range routes {
		actual, e := db.ResolveAPIKey(ctx, route.Key.KeyHMAC)
		if e != nil {
			t.Fatal(e)
		}
		counts[actual.Account.ID]++
		if actual.Account.ID == accounts[1].ID {
			movedRoute = route
		}
		// Reusing the original (stale) route must not revert a concurrent assignment.
		selected, e := db.PreferQuotaAccount(ctx, route)
		if e != nil || selected.ID != actual.Account.ID {
			t.Fatalf("stale route changed default: %s %v", selected.ID, e)
		}
	}
	if counts[accounts[1].ID] != 1 || counts[accounts[2].ID] != 0 {
		t.Fatalf("unexpected assignments: %v", counts)
	}
	// When relative quota reverses, defaults must move back without migrating
	// continuations created on the previously preferred account.
	setupSettings.MaxAPIKeysPerAccount = 10
	if err = db.UpdateSettings(ctx, setupSettings); err != nil {
		t.Fatal(err)
	}
	for i, used := range []float64{25, 75} {
		if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testQuotaWithWindow(now, "weekly", used), now); err != nil {
			t.Fatal(err)
		}
	}
	reverseSessionHash := bytes.Repeat([]byte{221}, 32)
	if err = db.SaveSessionBinding(ctx, movedRoute.Key.ID, pool.ID, reverseSessionHash, accounts[1].ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	selected, err := db.PreferQuotaAccount(ctx, movedRoute)
	if err != nil || selected.ID != accounts[0].ID || db.QuotaRoutingSwitches() != 2 {
		t.Fatalf("reverse routing: account=%s switches=%d err=%v", selected.ID, db.QuotaRoutingSwitches(), err)
	}
	reversePinned, err := db.ResolveSessionAccount(ctx, movedRoute.Key.ID, reverseSessionHash)
	if err != nil || reversePinned.ID != accounts[1].ID {
		t.Fatalf("reverse routing migrated session: %s %v", reversePinned.ID, err)
	}
	pinned, err := db.ResolveSessionAccount(ctx, routes[0].Key.ID, sessionHash)
	if err != nil || pinned.ID != accounts[0].ID {
		t.Fatalf("session migrated: %s %v", pinned.ID, err)
	}
}
