package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

const maxControlBody = 1 << 20

type Cipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
}
type OAuth interface {
	Start(string, int) (string, error)
	Exchange(context.Context, string, string) (codex.Credentials, string, int, error)
}

type Server struct {
	store     store.Store
	sessions  *auth.AdminSessions
	keys      *auth.APIKeys
	cipher    Cipher
	oauth     OAuth
	refresher credential.AccountRefresher
	sources   *auth.SourceResolver
}

func New(st store.Store, sessions *auth.AdminSessions, keys *auth.APIKeys, cipher Cipher, oauth OAuth, refresher credential.AccountRefresher, sources *auth.SourceResolver) *Server {
	return &Server{store: st, sessions: sessions, keys: keys, cipher: cipher, oauth: oauth, refresher: refresher, sources: sources}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/session", s.admin(s.session))
	mux.HandleFunc("POST /api/v1/auth/logout", s.admin(s.logout))
	mux.HandleFunc("POST /api/v1/provider-accounts/oauth/start", s.admin(s.oauthStart))
	mux.HandleFunc("GET /api/v1/provider-accounts/oauth/callback", s.oauthCallback)
	mux.HandleFunc("GET /api/v1/provider-accounts", s.admin(s.listProviderAccounts))
	mux.HandleFunc("PUT /api/v1/provider-accounts/{id}", s.admin(s.updateProviderAccount))
	mux.HandleFunc("DELETE /api/v1/provider-accounts/{id}", s.admin(s.deleteProviderAccount))
	mux.HandleFunc("POST /api/v1/provider-accounts/{id}/refresh", s.admin(s.refreshProviderAccount))
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
		if err != nil || !s.sessions.Valid(cookie.Value) {
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
	sessionID, ok := s.sessions.Authenticate(request.Username, request.Password, s.sources.SourceIP(r))
	if !ok {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.sessions.SetCookie(w, sessionID)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": true})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		s.sessions.Revoke(cookie.Value)
	}
	s.sessions.ClearCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
}

func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DisplayName string `json:"display_name"`
	}
	if !decode(w, r, &request) {
		return
	}
	authURL, err := s.oauth.Start(request.DisplayName, 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authorization_url": authURL})
}

func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	if providerError := r.URL.Query().Get("error"); providerError != "" {
		writeError(w, http.StatusBadRequest, "provider authorization failed")
		return
	}
	credentials, display, maxKeys, err := s.oauth.Exchange(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if display == "" {
		display = credentials.Email
	}
	if display == "" {
		display = "Codex account"
	}
	if credentials.AccountID == "" {
		writeError(w, http.StatusBadGateway, "provider identity is missing account_id")
		return
	}
	raw, _ := json.Marshal(credentials)
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
	account := domain.ProviderAccount{ID: accountID, Provider: domain.ProviderCodex, CredentialType: domain.CredentialSubscription, DisplayName: display, SubjectHMAC: s.keys.Digest("provider-subject:" + domain.ProviderCodex + ":" + credentials.AccountID), CredentialCiphertext: ciphertext, CredentialVersion: 1, Status: domain.AccountActive, MaxAPIKeys: maxKeys}
	if err = s.store.CreateProviderAccount(r.Context(), account); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeError(w, http.StatusConflict, "this Codex account is already imported")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to save provider account")
		return
	}
	s.audit(r.Context(), "provider_account.create", "provider_account", accountID, "success")
	http.Redirect(w, r, "/#/accounts", http.StatusSeeOther)
}

func (s *Server) listProviderAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.ListProviderAccounts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list provider accounts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": accounts})
}

func (s *Server) updateProviderAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DisplayName string `json:"display_name"`
		Status      string `json:"status"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.DisplayName = strings.TrimSpace(request.DisplayName)
	if request.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	if request.Status != domain.AccountActive && request.Status != domain.AccountDisabled {
		writeError(w, http.StatusBadRequest, "status must be active or disabled")
		return
	}
	account := domain.ProviderAccount{ID: r.PathValue("id"), DisplayName: request.DisplayName, Status: request.Status}
	if err := s.store.UpdateProviderAccount(r.Context(), account); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "provider_account.update", "provider_account", account.ID, "success")
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
	if err := s.store.UpdateSettings(r.Context(), settings); errors.Is(err, store.ErrCapacityExhausted) {
		writeError(w, http.StatusConflict, "the limit cannot be lower than an account's active API key assignments")
		return
	} else if err != nil {
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

func (s *Server) createPool(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name           string   `json:"name"`
		ModelAllowlist []string `json:"model_allowlist"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	request.ModelAllowlist = normalizeModelAllowlist(request.ModelAllowlist)
	if len(request.ModelAllowlist) == 0 {
		writeError(w, http.StatusBadRequest, "model_allowlist must contain at least one model")
		return
	}
	poolID, err := id.New()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create pool")
		return
	}
	pool := domain.Pool{ID: poolID, Name: request.Name, Provider: domain.ProviderCodex, Strategy: domain.StrategyLeastAssigned, ModelAllowlist: request.ModelAllowlist}
	if err = s.store.CreatePool(r.Context(), pool); err != nil {
		writeError(w, http.StatusConflict, "failed to create pool")
		return
	}
	s.audit(r.Context(), "pool.create", "pool", poolID, "success")
	writeJSON(w, http.StatusCreated, pool)
}
func (s *Server) updatePool(w http.ResponseWriter, r *http.Request) {
	var pool domain.Pool
	if !decode(w, r, &pool) {
		return
	}
	pool.ID = r.PathValue("id")
	pool.Provider = domain.ProviderCodex
	pool.Strategy = domain.StrategyLeastAssigned
	if strings.TrimSpace(pool.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	pool.ModelAllowlist = normalizeModelAllowlist(pool.ModelAllowlist)
	if len(pool.ModelAllowlist) == 0 {
		writeError(w, http.StatusBadRequest, "model_allowlist must contain at least one model")
		return
	}
	if err := s.store.UpdatePool(r.Context(), pool); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "pool.update", "pool", pool.ID, "success")
	writeJSON(w, http.StatusOK, pool)
}

func normalizeModelAllowlist(models []string) []string {
	result := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, exists := seen[model]; exists {
			continue
		}
		seen[model] = struct{}{}
		result = append(result, model)
	}
	return result
}
func (s *Server) listPools(w http.ResponseWriter, r *http.Request) {
	pools, err := s.store.ListPools(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list pools")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": pools})
}

func (s *Server) addPoolAccount(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ProviderAccountID string `json:"provider_account_id"`
		Weight            int    `json:"weight"`
		Priority          int    `json:"priority"`
		Enabled           *bool  `json:"enabled"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Weight == 0 {
		request.Weight = 1
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	membership := domain.PoolAccount{PoolID: r.PathValue("id"), ProviderAccountID: request.ProviderAccountID, Weight: request.Weight, Priority: request.Priority, Enabled: enabled}
	if err := s.store.AddPoolAccount(r.Context(), membership); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "pool.account.add", "pool", membership.PoolID, "success")
	writeJSON(w, http.StatusOK, membership)
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PoolID       string     `json:"pool_id"`
		EmployeeName string     `json:"employee_name"`
		Scopes       []string   `json:"scopes"`
		RateLimit    int        `json:"rate_limit"`
		ExpiresAt    *time.Time `json:"expires_at"`
	}
	if !decode(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.EmployeeName) == "" || request.PoolID == "" {
		writeError(w, http.StatusBadRequest, "pool_id and employee_name are required")
		return
	}
	if request.RateLimit < 0 {
		writeError(w, http.StatusBadRequest, "rate_limit cannot be negative")
		return
	}
	if !validScopes(request.Scopes) {
		writeError(w, http.StatusBadRequest, "scopes may contain only *, responses, chat_completions, or models")
		return
	}
	plain, digest, hint, err := s.keys.Generate()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate API key")
		return
	}
	keyID, err := id.New()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate API key")
		return
	}
	key := domain.APIKey{ID: keyID, PoolID: request.PoolID, EmployeeName: strings.TrimSpace(request.EmployeeName), KeyHMAC: digest, KeyHint: hint, Scopes: request.Scopes, RateLimit: request.RateLimit, ExpiresAt: request.ExpiresAt}
	accountID, err := s.store.CreateAPIKeyAndBind(r.Context(), key)
	if errors.Is(err, store.ErrCapacityExhausted) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "subpool_account_capacity_exhausted", "message": "all account API key slots are full"}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create API key")
		return
	}
	s.audit(r.Context(), "api_key.create", "api_key", keyID, "success")
	writeJSON(w, http.StatusCreated, map[string]any{"id": keyID, "api_key": plain, "key_hint": hint, "provider_account_id": accountID})
}
func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list API keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": keys})
}
func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	keyID := r.PathValue("id")
	if err := s.store.RevokeAPIKey(r.Context(), keyID); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "api_key.revoke", "api_key", keyID, "success")
	writeJSON(w, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) listUsage(w http.ResponseWriter, r *http.Request) {
	filter := domain.UsageFilter{APIKeyID: r.URL.Query().Get("api_key_id")}
	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		var value time.Time
		value, err = time.Parse("2006-01-02", raw)
		filter.From = &value
	}
	if err == nil {
		if raw := r.URL.Query().Get("to"); raw != "" {
			var value time.Time
			value, err = time.Parse("2006-01-02", raw)
			filter.To = &value
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "dates must use YYYY-MM-DD")
		return
	}
	rows, err := s.store.ListUsage(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (s *Server) audit(ctx context.Context, action, targetType, targetID, result string) {
	_ = s.store.Audit(ctx, domain.AuditEvent{Actor: "admin", Action: action, TargetType: targetType, TargetID: targetID, Result: result})
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxControlBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "operation failed")
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func validScopes(scopes []string) bool {
	for _, scope := range scopes {
		if scope != "*" && scope != "responses" && scope != "chat_completions" && scope != "models" {
			return false
		}
	}
	return true
}
