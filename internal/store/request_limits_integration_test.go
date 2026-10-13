package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/store/storedb"
)

func TestPostgresRequestBodySettingsPersistence(t *testing.T) {
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
	original, err := database.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.UpdateSettings(ctx, original); err != nil {
			t.Error(err)
		}
	}()
	saved := domain.Settings{MaxAPIKeysPerAccount: 4, MaxRequestBodyBytes: 128 << 20, MaxInflightRequestBodyBytes: 512 << 20}
	if err := database.UpdateSettings(ctx, saved); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	got, err := other.GetSettings(ctx)
	if err != nil || got.MaxRequestBodyBytes != saved.MaxRequestBodyBytes || got.MaxInflightRequestBodyBytes != saved.MaxInflightRequestBodyBytes {
		t.Fatalf("saved settings did not survive another connection: %#v %v", got, err)
	}
	invalid := saved
	invalid.MaxInflightRequestBodyBytes = saved.MaxRequestBodyBytes * 2
	if err := database.UpdateSettings(ctx, invalid); err == nil {
		t.Fatal("database constraint accepted an unsafe buffer budget")
	}
	got, err = database.GetSettings(ctx)
	if err != nil || got.MaxRequestBodyBytes != saved.MaxRequestBodyBytes || got.MaxInflightRequestBodyBytes != saved.MaxInflightRequestBodyBytes {
		t.Fatal("a rejected update changed the saved limits")
	}
}

func TestPostgresRequestBodySettingsSerializePartialUpdates(t *testing.T) {
	databaseURL := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	original, err := database.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := database.UpdateSettings(ctx, original); err != nil {
			t.Error(err)
		}
	}()
	defaults := domain.Settings{MaxRequestBodyBytes: 128 << 20, MaxInflightRequestBodyBytes: 512 << 20}
	if err := database.UpdateSettings(ctx, domain.Settings{MaxAPIKeysPerAccount: 3}); err != nil {
		t.Fatal(err)
	}
	initial, err := database.PatchSettings(ctx, domain.SettingsUpdate{MaxAPIKeysPerAccount: 4}, defaults)
	if err != nil || initial.MaxRequestBodyBytes != 0 || initial.MaxInflightRequestBodyBytes != 0 {
		t.Fatalf("capacity-only update froze defaults: %#v %v", initial, err)
	}
	blocker, err := database.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	queries := database.queries.WithTx(blocker)
	if _, err := queries.GetSettingsForUpdate(ctx); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, err := database.PatchSettings(ctx, domain.SettingsUpdate{MaxAPIKeysPerAccount: 5}, defaults)
		completed <- err
	}()
	select {
	case err := <-completed:
		t.Fatalf("settings update did not wait for the row lock: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := queries.UpdateSettings(ctx, storedb.UpdateSettingsParams{
		MaxApiKeysPerAccount: 4, MaxRequestBodyBytes: 256 << 20, MaxInflightRequestBodyBytes: 1 << 30,
	}); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
	current, err := database.GetSettings(ctx)
	if err != nil || current.MaxAPIKeysPerAccount != 5 || current.MaxRequestBodyBytes != 256<<20 || current.MaxInflightRequestBodyBytes != 1<<30 {
		t.Fatalf("capacity update overwrote newly committed limits: %#v %v", current, err)
	}
	invalidBudget := int64(512 << 20)
	_, err = database.PatchSettings(ctx, domain.SettingsUpdate{MaxAPIKeysPerAccount: 6, MaxInflightRequestBodyBytes: &invalidBudget}, defaults)
	if !errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("partial update did not validate the latest limits: %v", err)
	}
	after, err := database.GetSettings(ctx)
	if err != nil || after != current {
		t.Fatalf("a rejected partial update changed settings: %#v %v", after, err)
	}
}
