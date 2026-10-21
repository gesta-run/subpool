package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/catalog"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
)

type AntigravityOAuth interface {
	Start() (antigravity.Authorization, error)
	Complete(context.Context, string, string) (antigravity.Credentials, error)
}

func (s *Server) WithAntigravity(oauth AntigravityOAuth, models catalog.AntigravityModels) *Server {
	s.antigravityOAuth = oauth
	s.catalog.WithAntigravity(models)
	return s
}

func (s *Server) startAntigravityOAuth(w http.ResponseWriter, r *http.Request) {
	if s.antigravityOAuth == nil {
		writeError(w, http.StatusServiceUnavailable, "Google Antigravity authorization is unavailable")
		return
	}
	authorization, err := s.antigravityOAuth.Start()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, authorization)
}

func (s *Server) completeAntigravityOAuth(w http.ResponseWriter, r *http.Request) {
	var input struct {
		LoginID     string `json:"login_id"`
		CallbackURL string `json:"callback_url"`
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &input) {
		return
	}
	if s.antigravityOAuth == nil {
		writeError(w, http.StatusServiceUnavailable, "Google Antigravity authorization is unavailable")
		return
	}
	if input.LoginID == "" || input.CallbackURL == "" || strings.TrimSpace(input.DisplayName) == "" {
		writeError(w, http.StatusBadRequest, "login_id, callback_url and display_name are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	credentials, err := s.antigravityOAuth.Complete(ctx, input.LoginID, input.CallbackURL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Google authorization could not be completed. Check the callback URL, account eligibility and OAuth configuration, then start again.")
		return
	}
	accountID, err := s.saveAntigravityAccount(ctx, credentials, strings.TrimSpace(input.DisplayName))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "provider_account.create", "provider_account", accountID, "success")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]string{"id": accountID})
}

func (s *Server) saveAntigravityAccount(ctx context.Context, credentials antigravity.Credentials, display string) (string, error) {
	if credentials.Subject == "" || credentials.ProjectID == "" || credentials.RefreshToken == "" || credentials.AccessToken == "" {
		return "", errors.New("incomplete Google authorization")
	}
	raw, err := json.Marshal(credentials)
	if err != nil {
		return "", err
	}
	encrypted, err := s.cipher.Encrypt(raw)
	if err != nil {
		return "", err
	}
	accountID, err := id.New()
	if err != nil {
		return "", err
	}
	account := domain.ProviderAccount{ID: accountID, Provider: domain.ProviderAntigravity, CredentialType: domain.CredentialSubscription,
		DisplayName: display, Email: credentials.Email, SubjectHMAC: s.keys.Digest("provider-subject:" + domain.ProviderAntigravity + ":" + credentials.Subject),
		CredentialCiphertext: encrypted, CredentialVersion: 1, Status: domain.AccountActive}
	if s.health != nil {
		result := s.health.Check(ctx, account)
		s.health.ApplyNewAccount(&account, result)
	}
	err = s.store.CreateProviderAccount(ctx, account)
	return accountID, err
}
