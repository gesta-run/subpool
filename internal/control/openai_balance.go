package control

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type openAIBalance interface {
	Usage(context.Context, openaibilling.Credentials) (openaibilling.Snapshot, error)
	VerifyAccount(context.Context, string, string) error
}

func (s *Server) openAICredentials(account domain.ProviderAccount) (openaicompat.Credentials, bool) {
	var credentials openaicompat.Credentials
	if account.Provider != domain.ProviderOpenAICompatible {
		return credentials, false
	}
	raw, err := s.cipher.Decrypt(account.CredentialCiphertext)
	if err != nil || json.Unmarshal(raw, &credentials) != nil {
		return credentials, false
	}
	return credentials, openaibilling.SupportsURL(credentials.BaseURL)
}

func (s *Server) describeOpenAIBalance(ctx context.Context, account *domain.ProviderAccount) {
	if account.Provider != domain.ProviderOpenAICompatible {
		return
	}
	stored, err := s.store.GetProviderAccount(ctx, account.ID)
	if err != nil {
		return
	}
	credentials, ok := s.openAICredentials(stored)
	if !ok {
		return
	}
	account.QuotaProvider = openaibilling.PlanType
	account.QuotaAuthorized = credentials.OpenAIBilling != nil
	if account.QuotaAuthorized {
		account.QuotaAuthorizationMethod = "browser"
		if credentials.OpenAIBilling.AdminKey != "" {
			account.QuotaAuthorizationMethod = "costs"
		}
	}
}

func (s *Server) configureOpenAIBalance(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.GetProviderAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	credentials, supported := s.openAICredentials(account)
	if !supported {
		writeError(w, http.StatusBadRequest, "balance integration requires the official OpenAI API endpoint")
		return
	}
	var snapshot openaibilling.Snapshot
	if r.Method == http.MethodDelete {
		credentials.OpenAIBilling = nil
	} else {
		var request openaibilling.Credentials
		if !decode(w, r, &request) {
			return
		}
		request.SessionKey = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(request.SessionKey), "Bearer "))
		request.OrganizationID = strings.TrimSpace(request.OrganizationID)
		request.AdminKey = strings.TrimSpace(request.AdminKey)
		if !request.Valid() {
			writeError(w, http.StatusBadRequest, "provide an organization ID and either browser authorization or an Admin key with grant amount, start and expiry")
			return
		}
		client := s.openaiBalance
		if client == nil {
			client = openaibilling.NewClient(nil)
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err = client.VerifyAccount(ctx, credentials.APIKey, request.OrganizationID); err != nil {
			writeError(w, http.StatusBadGateway, "could not verify this API key belongs to the billing organization")
			return
		}
		if snapshot, err = client.Usage(ctx, request); err != nil {
			writeError(w, http.StatusBadGateway, "billing query failed; check the credential, organization and Costs read access")
			return
		}
		credentials.OpenAIBilling = &request
	}
	raw, err := json.Marshal(credentials)
	if err != nil {
		writeError(w, 500, "failed to encode billing credentials")
		return
	}
	ciphertext, err := s.cipher.Encrypt(raw)
	if err != nil {
		writeError(w, 500, "failed to encrypt billing credentials")
		return
	}
	saved, err := s.store.UpdateProviderCredentialCAS(r.Context(), account.ID, account.CredentialVersion, ciphertext, account.CredentialVersion+1)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !saved {
		writeError(w, http.StatusConflict, "account changed; retry billing authorization")
		return
	}
	s.audit(r.Context(), "provider_account.billing_credentials.update", "provider_account", account.ID, "success")
	if credentials.OpenAIBilling != nil {
		quota, _ := json.Marshal(snapshot)
		err = s.store.UpdateProviderDetails(r.Context(), account.ID, "", quota, time.Now())
	} else {
		err = s.store.SetProviderQuotaError(r.Context(), account.ID, "quota_authorization_required", time.Now())
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"quota_authorized": credentials.OpenAIBilling != nil})
}
