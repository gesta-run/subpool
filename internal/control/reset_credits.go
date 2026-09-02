package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

const (
	resetCreditsFreshFor = 10 * time.Minute
	resetRefreshLease    = time.Minute
)

func (s *Server) getResetCredits(w http.ResponseWriter, r *http.Request) {
	if s.resets == nil {
		writeError(w, http.StatusServiceUnavailable, "Codex reset service is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	account, err := s.store.GetProviderAccount(ctx, r.PathValue("id"))
	if err != nil {
		s.writeResetError(w, err)
		return
	}
	if account.Provider != domain.ProviderCodex || (account.CredentialType != "" && account.CredentialType != domain.CredentialSubscription) {
		s.writeResetError(w, errResetUnsupported)
		return
	}
	force := r.URL.Query().Get("refresh") == "true"
	claimedByRequest := false
	if !force {
		snapshot, checkedAt, cacheErr := s.store.GetProviderResetCredits(ctx, account.ID)
		if cacheErr != nil {
			writeStoreError(w, cacheErr)
			return
		}
		if checkedAt != nil {
			credits, decodeErr := decodeResetCredits(snapshot)
			if decodeErr != nil {
				writeError(w, http.StatusInternalServerError, "stored Codex reset credits are invalid")
				return
			}
			if checkedAt.Before(time.Now().UTC().Add(-resetCreditsFreshFor)) {
				s.refreshResetCreditsInBackground(account.ID)
			}
			writeJSON(w, http.StatusOK, map[string]any{"reset_credits": credits, "checked_at": checkedAt})
			return
		}
		claimed, claimErr := s.store.ClaimProviderResetCreditRefresh(ctx, account.ID, time.Now().UTC(), time.Now().UTC().Add(resetRefreshLease))
		if claimErr != nil {
			writeStoreError(w, claimErr)
			return
		}
		if !claimed {
			snapshot, checkedAt, cacheErr = s.waitForResetCredits(ctx, account.ID)
			if cacheErr != nil {
				writeStoreError(w, cacheErr)
				return
			}
			if checkedAt == nil {
				writeError(w, http.StatusServiceUnavailable, "Codex reset credits are being refreshed")
				return
			}
			credits, decodeErr := decodeResetCredits(snapshot)
			if decodeErr != nil {
				writeError(w, http.StatusInternalServerError, "stored Codex reset credits are invalid")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"reset_credits": credits, "checked_at": checkedAt})
			return
		}
		claimedByRequest = true
	}
	credits, checkedAt, err := s.refreshResetCredits(ctx, account.ID)
	if err != nil {
		if claimedByRequest {
			s.releaseResetRefreshClaim(account.ID)
		}
		s.writeResetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reset_credits": credits, "checked_at": checkedAt})
}

func decodeResetCredits(snapshot []byte) (*codex.ResetCreditsSummary, error) {
	var credits *codex.ResetCreditsSummary
	if len(snapshot) > 0 {
		if err := json.Unmarshal(snapshot, &credits); err != nil {
			return nil, err
		}
	}
	return credits, nil
}

func (s *Server) refreshResetCredits(ctx context.Context, accountID string) (*codex.ResetCreditsSummary, time.Time, error) {
	_, credentials, err := s.codexSubscriptionCredentials(ctx, accountID)
	if err != nil {
		return nil, time.Time{}, err
	}
	credits, err := s.resets.ReadResetCredits(ctx, credentials)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read Codex reset credits: %w", err)
	}
	checkedAt := time.Now().UTC()
	snapshot, err := json.Marshal(credits)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("encode Codex reset credits: %w", err)
	}
	if err = s.store.SetProviderResetCredits(ctx, accountID, snapshot, checkedAt); err != nil {
		return nil, time.Time{}, err
	}
	return credits, checkedAt, nil
}

func (s *Server) refreshResetCreditsInBackground(accountID string) {
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	claimed, err := s.store.ClaimProviderResetCreditRefresh(ctx, accountID, now.Add(-resetCreditsFreshFor), now.Add(resetRefreshLease))
	if err != nil || !claimed {
		cancel()
		return
	}
	go func() {
		defer cancel()
		if _, _, refreshErr := s.refreshResetCredits(ctx, accountID); refreshErr != nil {
			s.releaseResetRefreshClaim(accountID)
			slog.Warn("Codex reset credit refresh failed", "provider_account_id", accountID, "error", refreshErr)
		}
	}()
}

func (s *Server) releaseResetRefreshClaim(accountID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.store.ReleaseProviderResetCreditRefresh(ctx, accountID)
}

func (s *Server) waitForResetCredits(ctx context.Context, accountID string) ([]byte, *time.Time, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for attempts := 0; attempts < 30; attempts++ {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-ticker.C:
			snapshot, checkedAt, err := s.store.GetProviderResetCredits(ctx, accountID)
			if err != nil || checkedAt != nil {
				return snapshot, checkedAt, err
			}
		}
	}
	return nil, nil, nil
}

func (s *Server) consumeResetCredit(w http.ResponseWriter, r *http.Request) {
	if s.resets == nil {
		writeError(w, http.StatusServiceUnavailable, "Codex reset service is unavailable")
		return
	}
	var request struct {
		CreditID       string `json:"credit_id"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.CreditID = strings.TrimSpace(request.CreditID)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyKey == "" || len(request.IdempotencyKey) > 200 || len(request.CreditID) > 512 {
		writeError(w, http.StatusBadRequest, "a valid idempotency_key is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	account, credentials, err := s.codexSubscriptionCredentials(ctx, r.PathValue("id"))
	if err != nil {
		s.writeResetError(w, err)
		return
	}
	result, err := s.resets.ConsumeResetCredit(ctx, credentials, request.CreditID, request.IdempotencyKey)
	if err != nil {
		s.audit(r.Context(), "provider_account.reset_credit.consume", "provider_account", account.ID, "failure")
		writeError(w, http.StatusBadGateway, "failed to consume Codex reset credit")
		return
	}
	if snapshot, marshalErr := json.Marshal(result.ResetCredits); marshalErr == nil {
		_ = s.store.SetProviderResetCredits(ctx, account.ID, snapshot, time.Now().UTC())
	}
	auditResult := "failure"
	if result.Outcome == "reset" || result.Outcome == "alreadyRedeemed" {
		auditResult = "success"
		if account.Status == domain.AccountCoolingDown || account.Status == domain.AccountExhausted {
			_ = s.store.UpdateProviderStatus(r.Context(), account.ID, domain.AccountActive, nil)
		}
	}
	s.audit(r.Context(), "provider_account.reset_credit.consume", "provider_account", account.ID, auditResult)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) codexSubscriptionCredentials(ctx context.Context, accountID string) (domain.ProviderAccount, codex.Credentials, error) {
	account, err := s.store.GetProviderAccount(ctx, accountID)
	if err != nil {
		return domain.ProviderAccount{}, codex.Credentials{}, err
	}
	if account.Provider != domain.ProviderCodex || (account.CredentialType != "" && account.CredentialType != domain.CredentialSubscription) {
		return domain.ProviderAccount{}, codex.Credentials{}, errResetUnsupported
	}
	credentials, err := s.decryptCredentials(account)
	if err != nil {
		return domain.ProviderAccount{}, codex.Credentials{}, err
	}
	if !credentials.ExpiresAt.IsZero() && time.Until(credentials.ExpiresAt) <= 30*time.Second {
		account, err = s.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
		if err != nil {
			return domain.ProviderAccount{}, codex.Credentials{}, err
		}
		credentials, err = s.decryptCredentials(account)
		if err != nil {
			return domain.ProviderAccount{}, codex.Credentials{}, err
		}
	}
	return account, credentials, nil
}

var errResetUnsupported = errors.New("reset credits are supported only for Codex subscription accounts")

func (s *Server) writeResetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeStoreError(w, err)
	case errors.Is(err, errResetUnsupported):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusBadGateway, "failed to prepare Codex account credentials")
	}
}
