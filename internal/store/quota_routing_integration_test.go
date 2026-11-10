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

func TestPostgresQuotaRoutingRestoresHigherPriorityPrimary(t *testing.T) {
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
	db.WithCodexQuotaRouting(true)
	now := time.Now()
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000009310", Name: "Illustrative primary pool", Provider: domain.ProviderCodex}
	if err = db.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	accounts := make([]domain.ProviderAccount, 2)
	for i := range accounts {
		accounts[i] = domain.ProviderAccount{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 9300+i), Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative primary account", SubjectHMAC: bytes.Repeat([]byte{byte(231 + i)}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
		if err = db.CreateProviderAccount(ctx, accounts[i]); err != nil {
			t.Fatal(err)
		}
		if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: accounts[i].ID, Weight: 1, Priority: i * 10, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testQuota(now, 10, 10), now); err != nil {
			t.Fatal(err)
		}
	}
	key := domain.APIKey{ID: "00000000-0000-4000-8000-000000009320", PoolID: pool.ID, EmployeeName: "Illustrative primary user", KeyHMAC: bytes.Repeat([]byte{241}, 32), KeyHint: "test"}
	if _, err = db.CreateAPIKeyAndBind(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ReassignAPIKey(ctx, key.ID, pool.ID, []string{accounts[0].ID}); err != nil {
		t.Fatal(err)
	}
	sessionHash := bytes.Repeat([]byte{242}, 32)
	if err = db.SaveSessionBinding(ctx, key.ID, pool.ID, sessionHash, accounts[1].ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		used    float64
		checked time.Time
		want    int
	}{
		{"exhausted primary stays on fallback", 100, now, 1},
		{"stale primary stays on fallback", 10, now.Add(-time.Hour), 1},
		{"recovered primary overrides higher fallback quota", 99, now, 0},
		{"primary is not balanced away", 99, now, 0},
	} {
		if err = db.UpdateProviderDetails(ctx, accounts[0].ID, "", testQuota(now, 10, test.used), test.checked); err != nil {
			t.Fatal(err)
		}
		route, resolveErr := db.ResolveAPIKey(ctx, key.KeyHMAC)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		selected, selectErr := db.PreferQuotaAccount(ctx, route)
		if selectErr != nil || selected.ID != accounts[test.want].ID {
			t.Fatalf("%s: selected=%s err=%v", test.name, selected.ID, selectErr)
		}
	}
	pinned, err := db.ResolveSessionAccount(ctx, key.ID, sessionHash)
	if err != nil || pinned.ID != accounts[1].ID {
		t.Fatalf("continuation moved: account=%s err=%v", pinned.ID, err)
	}
}

func TestPostgresSubscriptionCreditsAndRecovery(t *testing.T) {
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
	db.WithCodexQuotaRouting(true)
	now := time.Now()
	accounts := make([]domain.ProviderAccount, 2)
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000009450", Name: "Illustrative prepaid routing pool", Provider: domain.ProviderCodex}
	if err = db.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for i := range accounts {
		accounts[i] = domain.ProviderAccount{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 9400+i), Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Illustrative prepaid account", SubjectHMAC: bytes.Repeat([]byte{byte(151 + i)}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}
		if err = db.CreateProviderAccount(ctx, accounts[i]); err != nil {
			t.Fatal(err)
		}
		if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: accounts[i].ID, Weight: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testCreditQuota(now, 100, "1000"), now); err != nil {
			t.Fatal(err)
		}
	}
	key := domain.APIKey{ID: "00000000-0000-4000-8000-000000009460", PoolID: pool.ID, EmployeeName: "Illustrative prepaid employee", KeyHMAC: bytes.Repeat([]byte{153}, 32), KeyHint: "test"}
	if _, err = db.CreateAPIKeyAndBind(ctx, key); err != nil {
		t.Fatal(err)
	}
	initial, err := db.ResolveAPIKey(ctx, key.KeyHMAC)
	if err != nil {
		t.Fatal(err)
	}
	secondKey := domain.APIKey{ID: "00000000-0000-4000-8000-000000009461", PoolID: pool.ID, EmployeeName: "Illustrative second employee", KeyHMAC: bytes.Repeat([]byte{155}, 32), KeyHint: "test"}
	secondID, err := db.CreateAPIKeyAndBind(ctx, secondKey)
	if err != nil || secondID == initial.Account.ID {
		t.Fatalf("equal budgets lost weighted allocation: %s %v", secondID, err)
	}
	// The remaining cases exercise budget ordering rather than a full account.
	if err = db.RevokeAPIKey(ctx, secondKey.ID); err != nil {
		t.Fatal(err)
	}
	session := bytes.Repeat([]byte{154}, 32)
	if err = db.SaveSessionBinding(ctx, key.ID, pool.ID, session, accounts[1].ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		used     [2]float64
		balances [2]string
		want     int
	}{
		{"subscription balancing", [2]float64{70, 30}, [2]string{"1000", "2000"}, 1},
		{"subscription before larger prepaid balance", [2]float64{99, 100}, [2]string{"1000", "2000"}, 0},
		{"both subscriptions depleted", [2]float64{100, 100}, [2]string{"1000", "2000"}, 1},
		{"credits balance reversal", [2]float64{100, 100}, [2]string{"3000", "2000"}, 0},
		{"restored subscription", [2]float64{100, 25}, [2]string{"3000", "2000"}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			for i := range accounts {
				if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testCreditQuota(now, test.used[i], test.balances[i]), now); err != nil {
					t.Fatal(err)
				}
			}
			// First exercise an existing independent request, then the retry allocator.
			route, e := db.ResolveAPIKey(ctx, key.KeyHMAC)
			if e != nil {
				t.Fatal(e)
			}
			selected, e := db.PreferQuotaAccount(ctx, route)
			if e != nil || selected.ID != accounts[test.want].ID {
				t.Fatalf("independent=%s want=%s err=%v", selected.ID, accounts[test.want].ID, e)
			}
			selected, e = db.ReassignAPIKey(ctx, key.ID, pool.ID, nil)
			if e != nil || selected.ID != accounts[test.want].ID {
				t.Fatalf("retry=%s want=%s err=%v", selected.ID, accounts[test.want].ID, e)
			}
			pinned, e := db.ResolveSessionAccount(ctx, key.ID, session)
			if e != nil || pinned.ID != accounts[1].ID {
				t.Fatalf("continuation migrated: %s %v", pinned.ID, e)
			}
		})
	}
	// A restored subscription outranks a credit-funded account even across
	// configured priorities. Priorities still apply within each budget tier.
	for i := range accounts {
		if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: accounts[i].ID, Weight: 1, Priority: (1 - i) * 10, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testCreditQuota(now, []float64{99, 100}[i], "2000"), now); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := db.ReassignAPIKey(ctx, key.ID, pool.ID, nil)
	if err != nil || selected.ID != accounts[0].ID {
		t.Fatalf("subscription priority: %s %v", selected.ID, err)
	}
	for i := range accounts {
		if err = db.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: accounts[i].ID, Weight: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	// Excluded attempts must not be selected even when they have more credits.
	for i := range accounts {
		if err = db.UpdateProviderDetails(ctx, accounts[i].ID, "", testCreditQuota(now, 100, []string{"3000", "2000"}[i]), now); err != nil {
			t.Fatal(err)
		}
	}
	selected, err = db.ReassignAPIKey(ctx, key.ID, pool.ID, []string{accounts[0].ID})
	if err != nil || selected.ID != accounts[1].ID {
		t.Fatalf("excluded retry=%s err=%v", selected.ID, err)
	}
	// Fresh health reports disable accounts when neither budget is available.
	for _, account := range accounts {
		if err = db.SetProviderUsageAllowed(ctx, account.ID, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ReassignAPIKey(ctx, key.ID, pool.ID, nil); err != ErrNoEligibleAccount {
		t.Fatalf("empty budgets did not stop routing: %v", err)
	}
}
