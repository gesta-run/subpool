package control

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
	"github.com/gesta-run/subpool/internal/store"
)

func (s *Server) createPool(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name               string   `json:"name"`
		ProviderAccountIDs []string `json:"provider_account_ids"`
	}
	if !decode(w, r, &request) {
		return
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	request.ProviderAccountIDs = normalizeIDs(request.ProviderAccountIDs)
	if len(request.ProviderAccountIDs) == 0 {
		writeError(w, http.StatusBadRequest, "provider_account_ids must contain at least one account")
		return
	}
	poolID, err := id.New()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create pool")
		return
	}
	provider := ""
	accounts := make([]domain.ProviderAccount, 0, len(request.ProviderAccountIDs))
	for _, accountID := range request.ProviderAccountIDs {
		account, getErr := s.store.GetProviderAccount(r.Context(), accountID)
		if getErr != nil {
			writeStoreError(w, getErr)
			return
		}
		accounts = append(accounts, account)
		provider = mergePoolProvider(provider, account.Provider)
	}
	pool := domain.Pool{ID: poolID, Name: request.Name, Provider: provider}
	for _, account := range accounts {
		pool.Accounts = append(pool.Accounts, domain.PoolAccount{PoolID: poolID, ProviderAccountID: account.ID, Weight: 1, Priority: defaultPoolPriority(account), Enabled: true})
	}
	if err = s.store.CreatePool(r.Context(), pool); errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "a pool with this name already exists")
		return
	} else if err != nil {
		writeStoreError(w, err)
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
	if strings.TrimSpace(pool.Name) == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if err := s.store.UpdatePool(r.Context(), pool); err != nil {
		writeStoreError(w, err)
		return
	}
	s.audit(r.Context(), "pool.update", "pool", pool.ID, "success")
	writeJSON(w, http.StatusOK, pool)
}

func normalizeIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, value := range ids {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mergePoolProvider(current, next string) string {
	if current == "" || current == next {
		return next
	}
	return domain.ProviderMixed
}

func defaultPoolPriority(account domain.ProviderAccount) int {
	if account.CredentialType == domain.CredentialAPIKey {
		return 100
	}
	return 0
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
		Priority          *int   `json:"priority"`
		Enabled           *bool  `json:"enabled"`
	}
	if !decode(w, r, &request) {
		return
	}
	if request.Weight == 0 {
		request.Weight = 1
	}
	if request.Weight < 1 || request.Weight > 100 {
		writeError(w, http.StatusBadRequest, "weight must be between 1 and 100")
		return
	}
	request.ProviderAccountID = strings.TrimSpace(request.ProviderAccountID)
	if request.ProviderAccountID == "" {
		writeError(w, http.StatusBadRequest, "provider_account_id is required")
		return
	}
	account, err := s.store.GetProviderAccount(r.Context(), request.ProviderAccountID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	priority := defaultPoolPriority(account)
	if request.Priority != nil {
		priority = *request.Priority
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	membership := domain.PoolAccount{PoolID: r.PathValue("id"), ProviderAccountID: request.ProviderAccountID, Weight: request.Weight, Priority: priority, Enabled: enabled}
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
	if errors.Is(err, store.ErrNoEligibleAccount) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "subpool_no_eligible_account", "message": "the pool has no eligible account"}})
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
	const (
		defaultLimit = 50
		maximumLimit = 100
		topKeyLimit  = 5
		maxCursorLen = 1024
	)

	filter := domain.UsageSummaryFilter{APIKeyID: strings.TrimSpace(r.URL.Query().Get("api_key_id")), Limit: defaultLimit + 1}
	if filter.APIKeyID != "" && !id.Valid(filter.APIKeyID) {
		writeError(w, http.StatusBadRequest, "api_key_id must be a UUID")
		return
	}
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maximumLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = value
		filter.Limit = limit + 1
	}

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
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		writeError(w, http.StatusBadRequest, "from must be on or before to")
		return
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > maxCursorLen {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cursor, decodeErr := decodeUsageCursor(raw)
		if decodeErr != nil {
			writeError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		filter.AfterTotal = &cursor.TotalTokens
		filter.AfterAPIKey = cursor.APIKeyID
		filter.AfterModel = cursor.Model
	}

	rows, err := s.store.ListUsageSummary(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list usage")
		return
	}
	totals, err := s.store.GetUsageTotals(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to summarize usage")
		return
	}
	topKeys, err := s.store.ListTopUsageKeys(r.Context(), filter, topKeyLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to summarize usage")
		return
	}

	nextCursor := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		nextCursor, err = encodeUsageCursor(usageCursor{
			TotalTokens: last.InputTokens + last.OutputTokens,
			APIKeyID:    last.APIKeyID,
			Model:       last.Model,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to paginate usage")
			return
		}
	}
	if rows == nil {
		rows = []domain.UsageSummary{}
	}
	if topKeys == nil {
		topKeys = []domain.UsageKeySummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"items": rows, "summary": totals, "top_keys": topKeys, "next_cursor": nextCursor,
	}})
}

type usageCursor struct {
	TotalTokens int64  `json:"t"`
	APIKeyID    string `json:"k"`
	Model       string `json:"m"`
}

func encodeUsageCursor(cursor usageCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeUsageCursor(value string) (usageCursor, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return usageCursor{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cursor usageCursor
	if err = decoder.Decode(&cursor); err != nil {
		return usageCursor{}, err
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return usageCursor{}, errors.New("cursor contains trailing data")
	}
	if cursor.TotalTokens < 0 || !id.Valid(cursor.APIKeyID) || strings.TrimSpace(cursor.Model) == "" {
		return usageCursor{}, errors.New("cursor fields are invalid")
	}
	return cursor, nil
}

func (s *Server) audit(ctx context.Context, action, targetType, targetID, result string) {
	if err := s.store.Audit(ctx, domain.AuditEvent{Actor: "admin", Action: action, TargetType: targetType, TargetID: targetID, Result: result}); err != nil {
		slog.Error("audit event write failed", "action", action, "target_type", targetType, "target_id", targetID, "result", result, "error", err)
	}
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxControlBody+1))
	if err != nil || len(body) > maxControlBody {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
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
