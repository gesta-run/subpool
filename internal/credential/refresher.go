package credential

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

type CodexTokenRefresher interface {
	Refresh(context.Context, string) (codex.Credentials, error)
}
type AccountRefresher interface {
	RefreshAccount(context.Context, string, int) (domain.ProviderAccount, error)
}

type AntigravityRefresher interface {
	Refresh(context.Context, antigravity.Credentials) (antigravity.Credentials, error)
}

type RefreshManager struct {
	store       store.Store
	cipher      *Cipher
	provider    CodexTokenRefresher
	antigravity AntigravityRefresher
	locks       sync.Map
}

func NewRefreshManager(st store.Store, cipher *Cipher, provider CodexTokenRefresher) *RefreshManager {
	return &RefreshManager{store: st, cipher: cipher, provider: provider}
}

func (m *RefreshManager) WithAntigravity(provider AntigravityRefresher) *RefreshManager {
	m.antigravity = provider
	return m
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
	raw, err := m.refreshCredentials(ctx, account.Provider, plaintext)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	ciphertext, err := m.cipher.Encrypt(raw)
	if err != nil {
		return domain.ProviderAccount{}, fmt.Errorf("encrypt provider credentials: %w", err)
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
	if account.Status != domain.AccountDisabled && account.Status != domain.AccountExhausted {
		account.Status = domain.AccountActive
		account.CooldownUntil = nil
	}
	return account, nil
}

func (m *RefreshManager) refreshCredentials(ctx context.Context, provider string, plaintext []byte) ([]byte, error) {
	if provider == domain.ProviderAntigravity {
		if m.antigravity == nil {
			return nil, fmt.Errorf("Antigravity refresh is unavailable")
		}
		var current antigravity.Credentials
		if err := json.Unmarshal(plaintext, &current); err != nil {
			return nil, fmt.Errorf("decode Antigravity credentials: %w", err)
		}
		refreshed, err := m.antigravity.Refresh(ctx, current)
		if err != nil {
			return nil, err
		}
		return json.Marshal(refreshed)
	}
	if provider != "" && provider != domain.ProviderCodex {
		return nil, fmt.Errorf("provider does not support OAuth refresh")
	}
	var current codex.Credentials
	var err error
	if err = json.Unmarshal(plaintext, &current); err != nil {
		return nil, fmt.Errorf("decode provider credentials: %w", err)
	}
	refreshed, err := m.provider.Refresh(ctx, current.RefreshToken)
	if err != nil {
		return nil, err
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
	return json.Marshal(refreshed)
}
