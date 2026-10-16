package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/bailian"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

func (s *Server) describeBailianQuota(ctx context.Context, account *domain.ProviderAccount) {
	if account.Provider != domain.ProviderOpenAICompatible {
		return
	}
	// ListProviderAccounts deliberately omits credential ciphertext.
	stored, err := s.store.GetProviderAccount(ctx, account.ID)
	if err != nil {
		return
	}
	credentials, ok := s.bailianCredentials(stored)
	if !ok {
		return
	}
	account.QuotaProvider = bailian.PlanType
	account.QuotaAuthorized = credentials.BailianQuota != nil
	account.QuotaAuthorizationMethod = ""
	account.QuotaAccessKeyIDHint = ""
	if quota := credentials.BailianQuota; quota != nil {
		if quota.AccessToken != "" {
			account.QuotaAuthorizationMethod = "browser"
		} else if quota.AccessKeyID != "" && quota.AccessKeySecret != "" {
			account.QuotaAuthorizationMethod = "ram"
			account.QuotaAccessKeyIDHint = "****"
			if id := []rune(quota.AccessKeyID); len(id) > 8 {
				account.QuotaAccessKeyIDHint += string(id[len(id)-4:])
			}
		}
	}
}

func (s *Server) bailianCredentials(account domain.ProviderAccount) (openaicompat.Credentials, bool) {
	var credentials openaicompat.Credentials
	if account.Provider != domain.ProviderOpenAICompatible {
		return credentials, false
	}
	raw, err := s.cipher.Decrypt(account.CredentialCiphertext)
	if err != nil || json.Unmarshal(raw, &credentials) != nil {
		return credentials, false
	}
	return credentials, bailian.SupportsURL(credentials.BaseURL)
}

func (s *Server) configureBailianQuota(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.GetProviderAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	credentials, supported := s.bailianCredentials(account)
	if !supported {
		writeError(w, http.StatusBadRequest, "quota integration requires a Beijing Token Plan endpoint")
		return
	}
	var snapshot bailian.Snapshot
	if r.Method == http.MethodDelete {
		credentials.BailianQuota = nil
	} else {
		var request bailian.Credentials
		if !decode(w, r, &request) {
			return
		}
		request.AccessKeyID = strings.TrimSpace(request.AccessKeyID)
		request.AccessKeySecret = strings.TrimSpace(request.AccessKeySecret)
		request.AccessToken = strings.TrimSpace(request.AccessToken)
		if !request.Valid() {
			writeError(w, http.StatusBadRequest, "provide an access_token or both access_key_id and access_key_secret")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		client := s.bailianQuota
		if client == nil {
			client = bailian.NewClient(nil)
		}
		if snapshot, err = client.Usage(ctx, request); err != nil {
			writeError(w, http.StatusBadGateway, "quota authorization failed; sign in again with the subscription owner or grant the RAM user Token Plan access")
			return
		}
		credentials.BailianQuota = &request
	}
	raw, err := json.Marshal(credentials)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode quota credentials")
		return
	}
	ciphertext, err := s.cipher.Encrypt(raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt quota credentials")
		return
	}
	saved, err := s.store.UpdateProviderCredentialCAS(r.Context(), account.ID, account.CredentialVersion, ciphertext, account.CredentialVersion+1)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !saved {
		writeError(w, http.StatusConflict, "account changed; retry quota authorization")
		return
	}
	s.audit(r.Context(), "provider_account.quota_credentials.update", "provider_account", account.ID, "success")
	if credentials.BailianQuota != nil {
		quota, _ := json.Marshal(snapshot)
		err = s.store.UpdateProviderDetails(r.Context(), account.ID, "", quota, time.Now())
	} else {
		err = s.store.SetProviderQuotaError(r.Context(), account.ID, "quota_authorization_required", time.Now())
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"quota_authorized": credentials.BailianQuota != nil})
}
