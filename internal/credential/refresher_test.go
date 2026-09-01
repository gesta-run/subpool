package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

type refreshStore struct {
	store.Store
	mu      sync.Mutex
	account domain.ProviderAccount
}

func (s *refreshStore) GetProviderAccount(context.Context, string) (domain.ProviderAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.account, nil
}
func (s *refreshStore) UpdateProviderCredentialCAS(_ context.Context, _ string, expected int, ciphertext []byte, version int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.account.CredentialVersion != expected {
		return false, nil
	}
	s.account.CredentialCiphertext = ciphertext
	s.account.CredentialVersion = version
	return true, nil
}

type tokenRefresher struct {
	mu    sync.Mutex
	calls int
}

func (r *tokenRefresher) Refresh(context.Context, string) (codex.Credentials, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return codex.Credentials{AccessToken: "new-access", RefreshToken: "new-refresh", AccountID: "account-subject"}, nil
}

func TestRefreshManagerCoalescesObservedVersion(t *testing.T) {
	cipher, err := New(bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(codex.Credentials{AccessToken: "old-access", RefreshToken: "old-refresh", AccountID: "account-subject"})
	encrypted, _ := cipher.Encrypt(raw)
	st := &refreshStore{account: domain.ProviderAccount{ID: "account-1", CredentialCiphertext: encrypted, CredentialVersion: 1, Status: domain.AccountActive}}
	provider := &tokenRefresher{}
	manager := NewRefreshManager(st, cipher, provider)
	var wait sync.WaitGroup
	wait.Add(2)
	errorsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wait.Done()
			_, refreshErr := manager.RefreshAccount(context.Background(), "account-1", 1)
			errorsCh <- refreshErr
		}()
	}
	wait.Wait()
	close(errorsCh)
	for refreshErr := range errorsCh {
		if refreshErr != nil {
			t.Fatal(refreshErr)
		}
	}
	provider.mu.Lock()
	calls := provider.calls
	provider.mu.Unlock()
	if calls != 1 {
		t.Fatalf("provider refresh calls = %d", calls)
	}
	if st.account.CredentialVersion != 2 {
		t.Fatalf("credential version = %d", st.account.CredentialVersion)
	}
}
