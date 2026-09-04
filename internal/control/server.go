package control

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/catalog"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	providerhealth "github.com/gesta-run/subpool/internal/health"
	"github.com/gesta-run/subpool/internal/id"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

const maxControlBody = 1 << 20

type Cipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
}
type ResetCredits interface {
	ReadResetCredits(context.Context, codex.Credentials) (*codex.ResetCreditsSummary, error)
	ConsumeResetCredit(context.Context, codex.Credentials, string, string) (codex.ConsumeResetCreditResult, error)
}
type Server struct {
	store                  store.Store
	sessions               *auth.AdminSessions
	keys                   *auth.APIKeys
	cipher                 Cipher
	deviceAuth             DeviceAuth
	resets                 ResetCredits
	catalog                *catalog.Service
	refresher              credential.AccountRefresher
	sources                *auth.SourceResolver
	health                 *providerhealth.Checker
	onAccountRoutingChange func(string)
	loginMu                sync.Mutex
	logins                 map[string]*deviceLoginAttempt
}

func New(st store.Store, sessions *auth.AdminSessions, keys *auth.APIKeys, cipher Cipher, deviceAuth DeviceAuth, refresher credential.AccountRefresher, sources *auth.SourceResolver, healthChecker ...*providerhealth.Checker) *Server {
	var checker *providerhealth.Checker
	if len(healthChecker) > 0 {
		checker = healthChecker[0]
	}
	return &Server{store: st, sessions: sessions, keys: keys, cipher: cipher, deviceAuth: deviceAuth, refresher: refresher, sources: sources, health: checker, logins: make(map[string]*deviceLoginAttempt)}
}

func (s *Server) WithResetCredits(resets ResetCredits) *Server {
	s.resets = resets
	return s
}

func (s *Server) WithModelProviders(codexModels catalog.CodexModels, compatibleModels catalog.CompatibleModels) *Server {
	s.catalog = catalog.New(s.cipher, s.refresher, codexModels, compatibleModels)
	return s
}

func (s *Server) WithAccountRoutingChange(callback func(string)) *Server {
	s.onAccountRoutingChange = callback
	return s
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/session", s.admin(s.session))
	mux.HandleFunc("POST /api/v1/auth/logout", s.admin(s.logout))
	mux.HandleFunc("POST /api/v1/provider-accounts/codex/device-login", s.admin(s.startDeviceLogin))
	mux.HandleFunc("GET /api/v1/provider-accounts/codex/device-login/{id}", s.admin(s.getDeviceLogin))
	mux.HandleFunc("DELETE /api/v1/provider-accounts/codex/device-login/{id}", s.admin(s.cancelDeviceLogin))
	mux.HandleFunc("GET /api/v1/provider-accounts", s.admin(s.listProviderAccounts))
	mux.HandleFunc("GET /api/v1/provider-accounts/{id}/models", s.admin(s.listProviderModels))
	mux.HandleFunc("POST /api/v1/provider-accounts", s.admin(s.createProviderAccount))
	mux.HandleFunc("PUT /api/v1/provider-accounts/{id}", s.admin(s.updateProviderAccount))
	mux.HandleFunc("DELETE /api/v1/provider-accounts/{id}", s.admin(s.deleteProviderAccount))
	mux.HandleFunc("POST /api/v1/provider-accounts/{id}/refresh", s.admin(s.refreshProviderAccount))
	mux.HandleFunc("POST /api/v1/provider-accounts/{id}/check", s.admin(s.checkProviderAccount))
	mux.HandleFunc("GET /api/v1/provider-accounts/{id}/reset-credits", s.admin(s.getResetCredits))
	mux.HandleFunc("POST /api/v1/provider-accounts/{id}/reset-credits/consume", s.admin(s.consumeResetCredit))
	mux.HandleFunc("GET /api/v1/settings", s.admin(s.getSettings))
	mux.HandleFunc("PUT /api/v1/settings", s.admin(s.updateSettings))
	mux.HandleFunc("GET /api/v1/pools", s.admin(s.listPools))
	mux.HandleFunc("POST /api/v1/pools", s.admin(s.createPool))
	mux.HandleFunc("PUT /api/v1/pools/{id}", s.admin(s.updatePool))
	mux.HandleFunc("POST /api/v1/pools/{id}/accounts", s.admin(s.addPoolAccount))
	mux.HandleFunc("GET /api/v1/api-keys", s.admin(s.listAPIKeys))
	mux.HandleFunc("POST /api/v1/api-keys", s.admin(s.createAPIKey))
	mux.HandleFunc("POST /api/v1/api-keys/{id}/revoke", s.admin(s.revokeAPIKey))
	mux.HandleFunc("GET /api/v1/usage", s.admin(s.listUsage))
}

func (s *Server) session(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}

func (s *Server) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		valid, err := s.sessions.Valid(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "authentication is temporarily unavailable")
			return
		}
		if !valid {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &request) {
		return
	}
	sessionID, ok, err := s.sessions.Authenticate(r.Context(), request.Username, request.Password, s.sources.SourceIP(r))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "authentication is temporarily unavailable")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.sessions.SetCookie(w, sessionID)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		_ = s.sessions.Revoke(r.Context(), cookie.Value)
	}
	s.sessions.ClearCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

var errProviderCredentialsRejected = errors.New("provider rejected the credentials")

func (s *Server) saveCodexAccount(ctx context.Context, credentials codex.Credentials, display string) (string, error) {
	if credentials.AccountID == "" {
		return "", errors.New("provider identity is missing account_id")
	}
	if display == "" {
		display = credentials.Email
	}
	if display == "" {
		display = "Codex account"
	}
	raw, err := json.Marshal(credentials)
	if err != nil {
		return "", err
	}
	ciphertext, err := s.cipher.Encrypt(raw)
	if err != nil {
		return "", err
	}
	accountID, err := id.New()
	if err != nil {
		return "", err
	}
	account := domain.ProviderAccount{ID: accountID, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: display, SubjectHMAC: s.keys.Digest("provider-subject:" + domain.ProviderCodex + ":" + credentials.AccountID), CredentialCiphertext: ciphertext, CredentialVersion: 1, Status: domain.AccountActive}
	if err = s.validateNewAccount(ctx, &account); err != nil {
		return "", err
	}
	if err = s.store.CreateProviderAccount(ctx, account); err != nil {
		return "", err
	}
	s.audit(ctx, "provider_account.create", "provider_account", accountID, "success")
	return accountID, nil
}

func (s *Server) createProviderAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Provider    string `json:"provider"`
		DisplayName string `json:"display_name"`
		BaseURL     string `json:"base_url"`
		APIKey      string `json:"api_key"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	request.BaseURL = strings.TrimRight(strings.TrimSpace(request.BaseURL), "/")
	request.APIKey = strings.TrimSpace(request.APIKey)
	if request.Provider != domain.ProviderOpenAICompatible {
		writeError(w, http.StatusBadRequest, "provider must be openai_compatible")
		return
	}
	parsed, err := url.Parse(request.BaseURL)
	if request.DisplayName == "" || request.APIKey == "" || err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		writeError(w, http.StatusBadRequest, "display_name, a valid base_url, and api_key are required")
		return
	}
	credentials := map[string]string{"base_url": request.BaseURL, "api_key": request.APIKey}
	raw, err := json.Marshal(credentials)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode credentials")
		return
	}
	ciphertext, err := s.cipher.Encrypt(raw)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt credentials")
		return
	}
	accountID, err := id.New()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create account")
		return
	}
	account := domain.ProviderAccount{
		ID:                   accountID,
		Provider:             domain.ProviderOpenAICompatible,
		CredentialType:       domain.CredentialAPIKey,
		DisplayName:          request.DisplayName,
		SubjectHMAC:          s.keys.Digest("provider-subject:" + request.Provider + ":" + request.BaseURL + "\x00" + request.APIKey),
		CredentialCiphertext: ciphertext,
		CredentialVersion:    1,
		Status:               domain.AccountActive,
	}
	if !s.checkNewAccount(r.Context(), &account, w) {
		return
	}
	if err = s.store.CreateProviderAccount(r.Context(), account); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "this endpoint and API key are already connected")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to save provider account")
		return
	}
	s.audit(r.Context(), "provider_account.create", "provider_account", accountID, "success")
	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) listProviderAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListProviderAccounts(r.Context())
	if err != nil {
		slog.Error("failed to list provider accounts", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list provider accounts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": accounts})
}

func (s *Server) listProviderModels(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.GetProviderAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	if s.catalog == nil {
		writeError(w, http.StatusServiceUnavailable, "model discovery is unavailable")
		return
	}
	models, err := s.catalog.ListAccount(ctx, account)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to load supported models")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": models})
}

func (s *Server) decryptCredentials(account domain.ProviderAccount) (codex.Credentials, error) {
	plaintext, err := s.cipher.Decrypt(account.CredentialCiphertext)
	if err != nil {
		return codex.Credentials{}, err
	}
	var credentials codex.Credentials
	if err = json.Unmarshal(plaintext, &credentials); err != nil {
		return codex.Credentials{}, err
	}
	return credentials, nil
}

func (s *Server) updateProviderAccount(w http.ResponseWriter, r *http.Request) {
	var request domain.ProviderAccountUpdate
	if !decode(w, r, &request) {
		return
	}
	if request.DisplayName != nil {
		displayName := strings.TrimSpace(*request.DisplayName)
		if displayName == "" {
			writeError(w, http.StatusBadRequest, "display_name is required")
			return
		}
		request.DisplayName = &displayName
	}
	if request.Status != nil && *request.Status != domain.AccountActive && *request.Status != domain.AccountDisabled {
		writeError(w, http.StatusBadRequest, "status must be active or disabled")
		return
	}
	account, err := s.store.GetProviderAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if request.FastModeEnabled != nil {
		if *request.FastModeEnabled && (account.Provider != domain.ProviderCodex || account.CredentialType != domain.CredentialSubscription) {
			writeError(w, http.StatusBadRequest, "fast mode is only available for Codex subscription accounts")
			return
		}
		account.FastModeEnabled = *request.FastModeEnabled
	}
	if request.DisplayName != nil {
		account.DisplayName = *request.DisplayName
	}
	if request.Status != nil {
		account.Status = *request.Status
	}
	if err := s.store.UpdateProviderAccount(r.Context(), account.ID, request); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "provider_account.update", "provider_account", account.ID, "success")
	if request.FastModeEnabled != nil && s.onAccountRoutingChange != nil {
		s.onAccountRoutingChange(account.ID)
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) updateSettings(w http.ResponseWriter, r *http.Request) {
	var request struct {
		MaxAPIKeysPerAccount int `json:"max_api_keys_per_account"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.MaxAPIKeysPerAccount < 1 || request.MaxAPIKeysPerAccount > 100 {
		writeError(w, http.StatusBadRequest, "max_api_keys_per_account must be between 1 and 100")
		return
	}
	settings := domain.Settings{MaxAPIKeysPerAccount: request.MaxAPIKeysPerAccount}
	if err := s.store.UpdateSettings(r.Context(), settings); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "settings.update", "settings", "global", "success")
	updated, err := s.store.GetSettings(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteProviderAccount(w http.ResponseWriter, r *http.Request) {
	accountID := r.PathValue("id")
	if err := s.store.DeleteProviderAccount(r.Context(), accountID); errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "remove assigned API keys before removing this account")
		return
	} else if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "provider_account.delete", "provider_account", accountID, "success")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshProviderAccount(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.GetProviderAccount(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if account.CredentialType != "" && account.CredentialType != domain.CredentialSubscription {
		writeError(w, http.StatusBadRequest, "static API key accounts do not support credential refresh")
		return
	}
	_, err = s.refresher.RefreshAccount(r.Context(), account.ID, account.CredentialVersion)
	if err != nil {
		status := domain.AccountCoolingDown
		cooldown := time.Now().Add(time.Minute)
		cooldownUntil := &cooldown
		if codex.IsDefinitiveAuthError(err) {
			status = domain.AccountAuthFailed
			cooldownUntil = nil
		}
		_ = s.store.UpdateProviderStatus(r.Context(), account.ID, status, cooldownUntil)
		s.audit(r.Context(), "provider_account.refresh", "provider_account", account.ID, "failure")
		writeError(w, http.StatusBadGateway, "provider credential refresh failed")
		return
	}
	s.audit(r.Context(), "provider_account.refresh", "provider_account", account.ID, "success")
	writeJSON(w, http.StatusOK, map[string]any{"status": "active"})
}

func (s *Server) checkProviderAccount(w http.ResponseWriter, r *http.Request) {
	if s.health == nil {
		writeError(w, http.StatusServiceUnavailable, "provider health checker is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	account, err := s.health.CheckAccount(ctx, r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "provider_account.check", "provider_account", account.ID, "success")
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) checkNewAccount(ctx context.Context, account *domain.ProviderAccount, w http.ResponseWriter) bool {
	if err := s.validateNewAccount(ctx, account); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func (s *Server) validateNewAccount(ctx context.Context, account *domain.ProviderAccount) error {
	if s.health == nil {
		account.HealthStatus = domain.HealthUnknown
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := s.health.Check(checkCtx, *account)
	if result.AuthFailed {
		return errProviderCredentialsRejected
	}
	s.health.ApplyNewAccount(account, result)
	return nil
}
