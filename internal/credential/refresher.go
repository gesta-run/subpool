package credential

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

type CodexTokenRefresher interface {
	Refresh(context.Context, string) (codex.Credentials, error)
}
type AccountRefresher interface {
	RefreshAccount(context.Context, string, int) (domain.ProviderAccount, error)
}

type RefreshManager struct {
	store    store.Store
	cipher   *Cipher
	provider CodexTokenRefresher
	locks    sync.Map
}

func NewRefreshManager(st store.Store, cipher *Cipher, provider CodexTokenRefresher) *RefreshManager {
	return &RefreshManager{store: st, cipher: cipher, provider: provider}
}

func (m *RefreshManager) RefreshAccount(ctx context.Context, accountID string, observedVersion int) (domain.ProviderAccount, error) {
	value, _ := m.locks.LoadOrStore(accountID, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	account, err := m.store.GetProviderAccount(ctx, accountID)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if account.CredentialVersion > observedVersion {
		return account, nil
	}
	plaintext, err := m.cipher.Decrypt(account.CredentialCiphertext)
	if err != nil {
		return domain.ProviderAccount{}, fmt.Errorf("decrypt provider credentials: %w", err)
	}
	var current codex.Credentials
	if err = json.Unmarshal(plaintext, &current); err != nil {
		return domain.ProviderAccount{}, fmt.Errorf("decode provider credentials: %w", err)
	}
	refreshed, err := m.provider.Refresh(ctx, current.RefreshToken)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = current.RefreshToken
	}
	if refreshed.AccountID == "" {
		refreshed.AccountID = current.AccountID
	}
	if refreshed.Email == "" {
		refreshed.Email = current.Email
	}
	if refreshed.IDToken == "" {
		refreshed.IDToken = current.IDToken
	}
	raw, err := json.Marshal(refreshed)
	if err != nil {
		return domain.ProviderAccount{}, fmt.Errorf("encode provider credentials: %w", err)
	}
	ciphertext, err := m.cipher.Encrypt(raw)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	updated, err := m.store.UpdateProviderCredentialCAS(ctx, account.ID, account.CredentialVersion, ciphertext, account.CredentialVersion+1)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if !updated {
		return m.store.GetProviderAccount(ctx, accountID)
	}
	account.CredentialCiphertext = ciphertext
	account.CredentialVersion++
	account.Status = domain.AccountActive
	account.CooldownUntil = nil
	return account, nil
}
