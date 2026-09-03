package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
)

func TestPostgresAdminLoginAttemptTracksInvalidCredentials(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := []string{"test-ip:203.0.113.1", "test-credential:admin|203.0.113.1"}
	accepted, err := database.RecordAdminLoginAttempt(ctx, keys, false, time.Now().UTC())
	if err != nil || accepted {
		t.Fatalf("invalid attempt accepted=%v error=%v", accepted, err)
	}
	accepted, err = database.RecordAdminLoginAttempt(ctx, keys, true, time.Now().UTC())
	if err != nil || !accepted {
		t.Fatalf("valid attempt accepted=%v error=%v", accepted, err)
	}
}

func TestPostgresHealthFailureThresholdAndRecovery(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account := domain.ProviderAccount{
		ID:                   "00000000-0000-4000-8000-000000000701",
		Provider:             domain.ProviderCodex,
		CredentialType:       domain.CredentialSubscription,
		DisplayName:          "Health threshold account",
		SubjectHMAC:          bytes.Repeat([]byte{71}, 32),
		CredentialCiphertext: []byte("encrypted"),
		CredentialVersion:    1,
		Status:               domain.AccountActive,
		HealthStatus:         domain.HealthHealthy,
	}
	if err = database.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000702", Name: "Health threshold pool", Provider: domain.ProviderCodex}
	if err = database.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: account.ID, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	checkedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for failure := 1; failure <= 3; failure++ {
		if err = database.RecordProviderHealthFailure(ctx, account.ID, "provider_unavailable", checkedAt, checkedAt.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		stored, getErr := database.GetProviderAccount(ctx, account.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		wantHealth := domain.HealthHealthy
		if failure == 3 {
			wantHealth = domain.HealthUnhealthy
		}
		if stored.ConsecutiveFailures != failure || stored.HealthStatus != wantHealth || stored.LastHealthErrorCode != "provider_unavailable" {
			t.Fatalf("failure %d account = %#v", failure, stored)
		}
	}
	eligible, err := database.ListPoolProviderAccounts(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 0 {
		t.Fatalf("unhealthy account remained eligible: %#v", eligible)
	}

	if err = database.SetProviderHealth(ctx, account.ID, domain.HealthHealthy, "", checkedAt.Add(5*time.Minute), checkedAt.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	recovered, err := database.GetProviderAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.HealthStatus != domain.HealthHealthy || recovered.ConsecutiveFailures != 0 || recovered.LastHealthErrorCode != "" {
		t.Fatalf("recovered account = %#v", recovered)
	}
	eligible, err = database.ListPoolProviderAccounts(ctx, pool.ID)
	if err != nil || len(eligible) != 1 || eligible[0].ID != account.ID {
		t.Fatalf("eligible accounts after recovery = %#v, %v", eligible, err)
	}
}

func TestPostgresAssignmentAndUsage(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 3}); err != nil {
		t.Fatal(err)
	}
	account := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000001", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Test account", SubjectHMAC: bytes.Repeat([]byte{8}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive}
	if err = database.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	duplicate := account
	duplicate.ID = "00000000-0000-4000-8000-000000000099"
	if err = database.CreateProviderAccount(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate subject error = %v", err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000002", Name: "Test pool", Provider: domain.ProviderCodex}
	if err = database.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: account.ID, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	keys := []domain.APIKey{
		{ID: "00000000-0000-4000-8000-000000000011", PoolID: pool.ID, EmployeeName: "Employee A", KeyHMAC: bytes.Repeat([]byte{1}, 32), KeyHint: "0001"},
		{ID: "00000000-0000-4000-8000-000000000012", PoolID: pool.ID, EmployeeName: "Employee B", KeyHMAC: bytes.Repeat([]byte{2}, 32), KeyHint: "0002"},
		{ID: "00000000-0000-4000-8000-000000000013", PoolID: pool.ID, EmployeeName: "Employee C", KeyHMAC: bytes.Repeat([]byte{3}, 32), KeyHint: "0003"},
		{ID: "00000000-0000-4000-8000-000000000014", PoolID: pool.ID, EmployeeName: "Employee D", KeyHMAC: bytes.Repeat([]byte{4}, 32), KeyHint: "0004"},
	}
	for i := 0; i < 3; i++ {
		assigned, createErr := database.CreateAPIKeyAndBind(ctx, keys[i])
		if createErr != nil || assigned != account.ID {
			t.Fatalf("key %d assignment = %q, %v", i, assigned, createErr)
		}
	}
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 3}); err != nil {
		t.Fatalf("capacity setting update error = %v", err)
	}
	if _, err = database.CreateAPIKeyAndBind(ctx, keys[3]); !errors.Is(err, ErrNoEligibleAccount) {
		t.Fatalf("fourth assignment error = %v, want no eligible account", err)
	}
	if err = database.RevokeAPIKey(ctx, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = database.CreateAPIKeyAndBind(ctx, keys[3]); err != nil {
		t.Fatalf("assignment after revoke error = %v", err)
	}
	route, err := database.ResolveAPIKey(ctx, keys[2].KeyHMAC)
	if err != nil || route.Account.ID != account.ID || route.Pool.ID != pool.ID || !route.MembershipEnabled {
		t.Fatalf("route = %#v, %v", route, err)
	}
	sessionHash := bytes.Repeat([]byte{9}, 32)
	if err = database.SaveSessionBinding(ctx, keys[2].ID, pool.ID, sessionHash, account.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sessionAccount, resolveErr := database.ResolveSessionAccount(ctx, keys[2].ID, sessionHash); resolveErr != nil || sessionAccount.ID != account.ID {
		t.Fatalf("session account = %#v, %v", sessionAccount, resolveErr)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: account.ID, Weight: 1, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if route, err = database.ResolveAPIKey(ctx, keys[2].KeyHMAC); err != nil || route.MembershipEnabled {
		t.Fatalf("disabled route = %#v, %v", route, err)
	}
	if _, resolveErr := database.ResolveSessionAccount(ctx, keys[2].ID, sessionHash); !errors.Is(resolveErr, ErrNotFound) {
		t.Fatalf("disabled membership continuation error = %v", resolveErr)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: account.ID, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	assertUsageActivity(t, ctx, database, account, keys[2])
}

func assertUsageActivity(t *testing.T, ctx context.Context, database *Postgres, account domain.ProviderAccount, key domain.APIKey) {
	t.Helper()
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	firstUsageEvent := bytes.Repeat([]byte{10}, 32)
	if err := database.AddUsage(ctx, key.ID, firstUsageEvent, "test-model", day, 10, 4); err != nil {
		t.Fatal(err)
	}
	if err := database.AddUsage(ctx, key.ID, firstUsageEvent, "test-model", day, 10, 4); err != nil {
		t.Fatal(err)
	}
	if err := database.AddUsage(ctx, key.ID, bytes.Repeat([]byte{11}, 32), "test-model", day, 2, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListUsage(ctx, domain.UsageFilter{APIKeyID: key.ID})
	if err != nil || len(rows) != 1 || rows[0].InputTokens != 12 || rows[0].OutputTokens != 5 {
		t.Fatalf("usage = %#v, %v", rows, err)
	}
	listedKeys, err := database.ListAPIKeys(ctx)
	if err != nil || len(listedKeys) == 0 || listedKeys[0].ID != key.ID || listedKeys[0].LastUsedAt == nil || !listedKeys[0].LastUsedAt.Equal(day) {
		t.Fatalf("API keys with activity = %#v, %v", listedKeys, err)
	}
	requestAt := day.Add(time.Hour)
	if err = database.RecordRequestSuccess(ctx, account.ID, key.ID, requestAt); err != nil {
		t.Fatal(err)
	}
	listedKeys, err = database.ListAPIKeys(ctx)
	if err != nil || listedKeys[0].LastUsedAt == nil || !listedKeys[0].LastUsedAt.Equal(requestAt) {
		t.Fatalf("API key last used = %#v, %v", listedKeys, err)
	}
}

func TestPostgresConcurrentAssignmentHonorsCapacity(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 3}); err != nil {
		t.Fatal(err)
	}
	account := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000101", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Concurrent account", SubjectHMAC: bytes.Repeat([]byte{7}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive}
	if err = database.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000102", Name: "Concurrent pool", Provider: domain.ProviderCodex}
	if err = database.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: account.ID, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			key := domain.APIKey{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 200+i), PoolID: pool.ID, EmployeeName: fmt.Sprintf("Concurrent %d", i), KeyHMAC: bytes.Repeat([]byte{byte(20 + i)}, 32), KeyHint: fmt.Sprintf("%04d", i)}
			_, createErr := database.CreateAPIKeyAndBind(ctx, key)
			results <- createErr
		}()
	}
	wait.Wait()
	close(results)
	successes, noEligibleErrors := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if errors.Is(result, ErrNoEligibleAccount) {
			noEligibleErrors++
		} else {
			t.Fatalf("unexpected assignment error: %v", result)
		}
	}
	if successes != 3 || noEligibleErrors != 1 {
		t.Fatalf("successes=%d no_eligible_errors=%d", successes, noEligibleErrors)
	}
}

func TestPostgresMixedPoolPrefersSubscriptionPriority(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 3}); err != nil {
		t.Fatal(err)
	}
	first := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000301", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "First", SubjectHMAC: bytes.Repeat([]byte{31}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive}
	second := first
	second.ID = "00000000-0000-4000-8000-000000000302"
	second.DisplayName = "Second"
	second.Provider = domain.ProviderOpenAICompatible
	second.CredentialType = domain.CredentialAPIKey
	second.SubjectHMAC = bytes.Repeat([]byte{32}, 32)
	if err = database.CreateProviderAccount(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = database.CreateProviderAccount(ctx, second); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000303", Name: "Least assigned pool", Provider: domain.ProviderCodex}
	if err = database.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: first.ID, Weight: 1, Priority: 0, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	seed := domain.APIKey{ID: "00000000-0000-4000-8000-000000000304", PoolID: pool.ID, EmployeeName: "Seed", KeyHMAC: bytes.Repeat([]byte{33}, 32), KeyHint: "seed"}
	if assigned, createErr := database.CreateAPIKeyAndBind(ctx, seed); createErr != nil || assigned != first.ID {
		t.Fatalf("seed assignment=%s error=%v", assigned, createErr)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: second.ID, Weight: 1, Priority: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	pools, listErr := database.ListPools(ctx)
	var storedPool domain.Pool
	for _, candidate := range pools {
		if candidate.ID == pool.ID {
			storedPool = candidate
			break
		}
	}
	if listErr != nil || storedPool.Provider != domain.ProviderMixed {
		t.Fatalf("mixed pool=%#v error=%v", pools, listErr)
	}
	next := domain.APIKey{ID: "00000000-0000-4000-8000-000000000305", PoolID: pool.ID, EmployeeName: "Next", KeyHMAC: bytes.Repeat([]byte{34}, 32), KeyHint: "next"}
	if assigned, createErr := database.CreateAPIKeyAndBind(ctx, next); createErr != nil || assigned != first.ID {
		t.Fatalf("priority account=%s error=%v", assigned, createErr)
	}
}

func TestPostgresAPIKeyCapacityRoutesToAvailableAccount(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 3}); err != nil {
		t.Fatal(err)
	}
	primary := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000401", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Primary account", SubjectHMAC: bytes.Repeat([]byte{41}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive}
	secondary := primary
	secondary.ID = "00000000-0000-4000-8000-000000000405"
	secondary.DisplayName = "Secondary account"
	secondary.SubjectHMAC = bytes.Repeat([]byte{45}, 32)
	if err = database.CreateProviderAccount(ctx, primary); err != nil {
		t.Fatal(err)
	}
	if err = database.CreateProviderAccount(ctx, secondary); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000402", Name: "Capacity pool", Provider: domain.ProviderCodex}
	if err = database.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: primary.ID, Weight: 1, Priority: 0, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: secondary.ID, Weight: 1, Priority: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		key := domain.APIKey{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", 410+index), PoolID: pool.ID, EmployeeName: fmt.Sprintf("Employee %d", index+1), KeyHMAC: bytes.Repeat([]byte{byte(50 + index)}, 32), KeyHint: fmt.Sprintf("key%d", index+1)}
		assigned, createErr := database.CreateAPIKeyAndBind(ctx, key)
		want := primary.ID
		if index == 3 {
			want = secondary.ID
		}
		if createErr != nil || assigned != want {
			t.Fatalf("key %d assignment=%s want=%s error=%v", index+1, assigned, want, createErr)
		}
	}
}
