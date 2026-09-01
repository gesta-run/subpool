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
	account := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000001", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Test account", SubjectHMAC: bytes.Repeat([]byte{8}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, MaxAPIKeys: 3}
	if err = database.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	duplicate := account
	duplicate.ID = "00000000-0000-4000-8000-000000000099"
	if err = database.CreateProviderAccount(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate subject error = %v", err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000002", Name: "Test pool", Provider: domain.ProviderCodex, Strategy: domain.StrategyLeastAssigned, ModelAllowlist: []string{"gpt-test"}}
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
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 2}); !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("capacity reduction error = %v", err)
	}
	if _, err = database.CreateAPIKeyAndBind(ctx, keys[3]); !errors.Is(err, ErrCapacityExhausted) {
		t.Fatalf("fourth assignment error = %v", err)
	}
	if err = database.RevokeAPIKey(ctx, keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = database.CreateAPIKeyAndBind(ctx, keys[3]); err != nil {
		t.Fatalf("slot was not released: %v", err)
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
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	firstUsageEvent := bytes.Repeat([]byte{10}, 32)
	if err = database.AddUsage(ctx, keys[2].ID, firstUsageEvent, day, 10, 4); err != nil {
		t.Fatal(err)
	}
	if err = database.AddUsage(ctx, keys[2].ID, firstUsageEvent, day, 10, 4); err != nil {
		t.Fatal(err)
	}
	if err = database.AddUsage(ctx, keys[2].ID, bytes.Repeat([]byte{11}, 32), day, 2, 1); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListUsage(ctx, domain.UsageFilter{APIKeyID: keys[2].ID})
	if err != nil || len(rows) != 1 || rows[0].InputTokens != 12 || rows[0].OutputTokens != 5 {
		t.Fatalf("usage = %#v, %v", rows, err)
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
	account := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000101", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Concurrent account", SubjectHMAC: bytes.Repeat([]byte{7}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, MaxAPIKeys: 3}
	if err = database.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000102", Name: "Concurrent pool", Provider: domain.ProviderCodex, Strategy: domain.StrategyLeastAssigned}
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
	successes, capacityErrors := 0, 0
	for result := range results {
		if result == nil {
			successes++
		} else if errors.Is(result, ErrCapacityExhausted) {
			capacityErrors++
		} else {
			t.Fatalf("unexpected assignment error: %v", result)
		}
	}
	if successes != 3 || capacityErrors != 1 {
		t.Fatalf("successes=%d capacity_errors=%d", successes, capacityErrors)
	}
}

func TestPostgresLeastAssignedPrecedesPriority(t *testing.T) {
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
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 2}); err != nil {
		t.Fatal(err)
	}
	first := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000301", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "First", SubjectHMAC: bytes.Repeat([]byte{31}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, MaxAPIKeys: 3}
	second := first
	second.ID = "00000000-0000-4000-8000-000000000302"
	second.DisplayName = "Second"
	second.SubjectHMAC = bytes.Repeat([]byte{32}, 32)
	if err = database.CreateProviderAccount(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = database.CreateProviderAccount(ctx, second); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000303", Name: "Least assigned pool", Provider: domain.ProviderCodex, Strategy: domain.StrategyLeastAssigned}
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
	next := domain.APIKey{ID: "00000000-0000-4000-8000-000000000305", PoolID: pool.ID, EmployeeName: "Next", KeyHMAC: bytes.Repeat([]byte{34}, 32), KeyHint: "next"}
	if assigned, createErr := database.CreateAPIKeyAndBind(ctx, next); createErr != nil || assigned != second.ID {
		t.Fatalf("least-assigned account=%s error=%v", assigned, createErr)
	}
}

func TestPostgresConcurrentCapacityReductionAndAssignment(t *testing.T) {
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
	if err = database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 2}); err != nil {
		t.Fatal(err)
	}
	account := domain.ProviderAccount{ID: "00000000-0000-4000-8000-000000000401", Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: "Capacity account", SubjectHMAC: bytes.Repeat([]byte{41}, 32), CredentialCiphertext: []byte("encrypted"), CredentialVersion: 1, Status: domain.AccountActive, MaxAPIKeys: 2}
	if err = database.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	pool := domain.Pool{ID: "00000000-0000-4000-8000-000000000402", Name: "Capacity pool", Provider: domain.ProviderCodex, Strategy: domain.StrategyLeastAssigned, ModelAllowlist: []string{"gpt-test"}}
	if err = database.CreatePool(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err = database.AddPoolAccount(ctx, domain.PoolAccount{PoolID: pool.ID, ProviderAccountID: account.ID, Weight: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	seed := domain.APIKey{ID: "00000000-0000-4000-8000-000000000403", PoolID: pool.ID, EmployeeName: "Seed", KeyHMAC: bytes.Repeat([]byte{42}, 32), KeyHint: "seed"}
	if _, err = database.CreateAPIKeyAndBind(ctx, seed); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	go func() {
		results <- database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 1})
	}()
	go func() {
		key := domain.APIKey{ID: "00000000-0000-4000-8000-000000000404", PoolID: pool.ID, EmployeeName: "Concurrent", KeyHMAC: bytes.Repeat([]byte{43}, 32), KeyHint: "next"}
		_, createErr := database.CreateAPIKeyAndBind(ctx, key)
		results <- createErr
	}()
	successes, capacityErrors := 0, 0
	for range 2 {
		result := <-results
		if result == nil {
			successes++
		} else if errors.Is(result, ErrCapacityExhausted) {
			capacityErrors++
		} else {
			t.Fatalf("unexpected concurrent result: %v", result)
		}
	}
	if successes != 1 || capacityErrors != 1 {
		t.Fatalf("successes=%d capacity_errors=%d", successes, capacityErrors)
	}
	accounts, err := database.ListProviderAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range accounts {
		if current.ID == account.ID && current.AssignedAPIKeys > current.MaxAPIKeys {
			t.Fatalf("capacity invariant violated: assigned=%d max=%d", current.AssignedAPIKeys, current.MaxAPIKeys)
		}
	}
}
