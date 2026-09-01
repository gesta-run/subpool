package gateway

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

const (
	maxRequestBody = 32 << 20
	accountHeader  = "X-Subpool-Internal-Account-Id"
)

type Cipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
}

type CodexClient interface {
	Responses(context.Context, []byte, http.Header, codex.Credentials) (*http.Response, error)
}

type Server struct {
	store     store.Store
	keys      *auth.APIKeys
	cipher    Cipher
	codex     CodexClient
	refresher credential.AccountRefresher
	limiter   *rateLimiter
	now       func() time.Time
	eventSeq  atomic.Uint64
}

func New(st store.Store, keys *auth.APIKeys, cipher Cipher, client CodexClient, refresher credential.AccountRefresher) *Server {
	return &Server{store: st, keys: keys, cipher: cipher, codex: client, refresher: refresher, limiter: newRateLimiter(), now: time.Now}
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("GET /v1/models", s.handleModels)
}

type requestMeta struct {
	Model              string `json:"model"`
	Stream             bool   `json:"stream"`
	PreviousResponseID string `json:"previous_response_id"`
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	route, ok := s.authorize(w, r, "responses")
	if !ok {
		return
	}
	body, meta, ok := readRequest(w, r)
	if !ok {
		return
	}
	if !modelAllowed(meta.Model, route.Pool.ModelAllowlist) {
		writeOpenAIError(w, http.StatusBadRequest, "model is not allowed by this API key", "invalid_request_error")
		return
	}
	body = forceStream(body)
	resp, ok := s.call(w, r, route, body, meta.PreviousResponseID)
	if !ok {
		return
	}
	defer resp.Body.Close()
	accountID := resp.Header.Get(accountHeader)
	resp.Header.Del(accountHeader)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.proxyUpstreamError(w, resp)
		return
	}
	if meta.Stream {
		s.proxyResponsesStream(w, r, route.Key.ID, route.Pool.ID, accountID, resp)
		return
	}
	s.proxyResponsesJSON(w, r, route.Key.ID, route.Pool.ID, accountID, resp)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	route, ok := s.authorize(w, r, "chat_completions")
	if !ok {
		return
	}
	body, meta, ok := readRequest(w, r)
	if !ok {
		return
	}
	if !modelAllowed(meta.Model, route.Pool.ModelAllowlist) {
		writeOpenAIError(w, http.StatusBadRequest, "model is not allowed by this API key", "invalid_request_error")
		return
	}
	responseBody, err := chatToResponses(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	resp, ok := s.call(w, r, route, responseBody, "")
	if !ok {
		return
	}
	defer resp.Body.Close()
	resp.Header.Del(accountHeader)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.proxyUpstreamError(w, resp)
		return
	}
	if meta.Stream {
		s.proxyChatStream(w, r, route.Key.ID, meta.Model, resp)
		return
	}
	s.proxyChatJSON(w, r, route.Key.ID, meta.Model, resp)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	route, ok := s.authorize(w, r, "models")
	if !ok {
		return
	}
	models := route.Pool.ModelAllowlist
	data := make([]map[string]any, 0, len(models))
	for _, id := range models {
		data = append(data, map[string]any{"id": id, "object": "model", "owned_by": "subpool"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request, requiredScope string) (domain.KeyRoute, bool) {
	plain, err := auth.Bearer(r.Header.Get("Authorization"))
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid API key", "invalid_api_key")
		return domain.KeyRoute{}, false
	}
	route, err := s.store.ResolveAPIKey(r.Context(), s.keys.Digest(plain))
	if err != nil {
		writeOpenAIError(w, http.StatusUnauthorized, "invalid API key", "invalid_api_key")
		return domain.KeyRoute{}, false
	}
	if !s.limiter.Allow(route.Key.ID, route.Key.RateLimit, s.now()) {
		writeOpenAIError(w, http.StatusTooManyRequests, "API key rate limit exceeded", "subpool_rate_limited")
		return domain.KeyRoute{}, false
	}
	if !scopeAllowed(route.Key.Scopes, requiredScope) {
		writeOpenAIError(w, http.StatusForbidden, "API key scope does not allow this endpoint", "insufficient_scope")
		return domain.KeyRoute{}, false
	}
	return route, true
}

func scopeAllowed(scopes []string, required string) bool {
	if len(scopes) == 0 {
		return true
	}
	for _, scope := range scopes {
		if scope == "*" || scope == required {
			return true
		}
	}
	return false
}

func readRequest(w http.ResponseWriter, r *http.Request) ([]byte, requestMeta, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody+1))
	if err != nil || len(body) > maxRequestBody {
		writeOpenAIError(w, http.StatusRequestEntityTooLarge, "request body is too large", "invalid_request_error")
		return nil, requestMeta{}, false
	}
	var meta requestMeta
	if json.Unmarshal(body, &meta) != nil || strings.TrimSpace(meta.Model) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "model is required", "invalid_request_error")
		return nil, requestMeta{}, false
	}
	return body, meta, true
}

func forceStream(body []byte) []byte {
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		return body
	}
	value["stream"] = true
	if instructions, ok := value["instructions"]; !ok || instructions == nil {
		value["instructions"] = ""
	}
	out, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return out
}

func (s *Server) call(w http.ResponseWriter, r *http.Request, route domain.KeyRoute, body []byte, previousResponseID string) (*http.Response, bool) {
	account := route.Account
	continuation := previousResponseID != ""
	if continuation {
		var err error
		account, err = s.store.ResolveSessionAccount(r.Context(), route.Key.ID, sessionHash(previousResponseID))
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable")
			return nil, false
		}
	}
	if (!continuation && !route.MembershipEnabled) || !accountHealthy(account, s.now()) {
		if continuation {
			writeOpenAIError(w, http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable")
			return nil, false
		}
		var err error
		account, err = s.store.ReassignAPIKey(r.Context(), route.Key.ID, route.Pool.ID, account.ID)
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "no eligible account", "subpool_no_eligible_account")
			return nil, false
		}
	}
	for accountAttempt := 0; accountAttempt < 2; accountAttempt++ {
		credentials, err := s.credentials(account)
		if err != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to load provider credentials", "server_error")
			return nil, false
		}
		resp, err := s.codex.Responses(r.Context(), body, r.Header, credentials)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "provider request failed", "provider_error")
			return nil, false
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			_ = s.store.MarkProviderSuccess(r.Context(), account.ID)
			_ = s.store.TouchAPIKey(r.Context(), route.Key.ID)
			resp.Header.Set(accountHeader, account.ID)
			return resp, true
		}
		if resp.StatusCode == http.StatusUnauthorized {
			drainAndClose(resp)
			refreshed, refreshErr := s.refresh(r.Context(), account, credentials)
			definitive := false
			if refreshErr == nil {
				account = refreshed
				refreshedCredentials, credentialsErr := s.credentials(account)
				if credentialsErr != nil {
					writeOpenAIError(w, http.StatusInternalServerError, "failed to load provider credentials", "server_error")
					return nil, false
				}
				resp, err = s.codex.Responses(r.Context(), body, r.Header, refreshedCredentials)
				if err != nil {
					writeOpenAIError(w, http.StatusBadGateway, "provider request failed", "provider_error")
					return nil, false
				}
				if resp.StatusCode >= 200 && resp.StatusCode < 300 {
					_ = s.store.MarkProviderSuccess(r.Context(), account.ID)
					_ = s.store.TouchAPIKey(r.Context(), route.Key.ID)
					resp.Header.Set(accountHeader, account.ID)
					return resp, true
				}
				if resp.StatusCode != http.StatusUnauthorized {
					return s.handleRetryableStatus(w, r, route, account, body, continuation, accountAttempt, resp)
				}
				drainAndClose(resp)
				_ = s.store.UpdateProviderStatus(r.Context(), account.ID, domain.AccountAuthFailed, nil)
				definitive = true
			} else {
				definitive = s.recordRefreshFailure(r.Context(), account.ID, refreshErr)
			}
			if continuation || accountAttempt == 1 {
				writeRefreshFailure(w, definitive)
				return nil, false
			}
			account, err = s.store.ReassignAPIKey(r.Context(), route.Key.ID, route.Pool.ID, account.ID)
			if err != nil {
				writeRefreshFailure(w, definitive)
				return nil, false
			}
			continue
		}
		return s.handleRetryableStatus(w, r, route, account, body, continuation, accountAttempt, resp)
	}
	writeOpenAIError(w, http.StatusServiceUnavailable, "no eligible account", "subpool_no_eligible_account")
	return nil, false
}

func (s *Server) handleRetryableStatus(w http.ResponseWriter, r *http.Request, route domain.KeyRoute, account domain.ProviderAccount, body []byte, continuation bool, attempt int, resp *http.Response) (*http.Response, bool) {
	if resp.StatusCode != http.StatusTooManyRequests {
		resp.Header.Set(accountHeader, account.ID)
		return resp, true
	}
	retryAt := retryAfter(resp.Header, s.now())
	drainAndClose(resp)
	_ = s.store.UpdateProviderStatus(r.Context(), account.ID, domain.AccountCoolingDown, &retryAt)
	if continuation || attempt == 1 {
		writeOpenAIError(w, http.StatusTooManyRequests, "all eligible accounts are rate limited", "subpool_rate_limited")
		return nil, false
	}
	next, err := s.store.ReassignAPIKey(r.Context(), route.Key.ID, route.Pool.ID, account.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusTooManyRequests, "all eligible accounts are rate limited", "subpool_rate_limited")
		return nil, false
	}
	credentials, err := s.credentials(next)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to load provider credentials", "server_error")
		return nil, false
	}
	nextResp, err := s.codex.Responses(r.Context(), body, r.Header, credentials)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "provider request failed", "provider_error")
		return nil, false
	}
	if nextResp.StatusCode == http.StatusTooManyRequests {
		nextAt := retryAfter(nextResp.Header, s.now())
		drainAndClose(nextResp)
		_ = s.store.UpdateProviderStatus(r.Context(), next.ID, domain.AccountCoolingDown, &nextAt)
		writeOpenAIError(w, http.StatusTooManyRequests, "all eligible accounts are rate limited", "subpool_rate_limited")
		return nil, false
	}
	if nextResp.StatusCode == http.StatusUnauthorized {
		drainAndClose(nextResp)
		refreshed, refreshErr := s.refresh(r.Context(), next, credentials)
		if refreshErr != nil {
			writeRefreshFailure(w, s.recordRefreshFailure(r.Context(), next.ID, refreshErr))
			return nil, false
		}
		next = refreshed
		refreshedCredentials, credentialsErr := s.credentials(next)
		if credentialsErr != nil {
			writeOpenAIError(w, http.StatusInternalServerError, "failed to load provider credentials", "server_error")
			return nil, false
		}
		nextResp, err = s.codex.Responses(r.Context(), body, r.Header, refreshedCredentials)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "provider request failed", "provider_error")
			return nil, false
		}
		if nextResp.StatusCode == http.StatusUnauthorized {
			drainAndClose(nextResp)
			_ = s.store.UpdateProviderStatus(r.Context(), next.ID, domain.AccountAuthFailed, nil)
			writeOpenAIError(w, http.StatusUnauthorized, "provider authentication failed", "provider_authentication_error")
			return nil, false
		}
		if nextResp.StatusCode == http.StatusTooManyRequests {
			nextAt := retryAfter(nextResp.Header, s.now())
			drainAndClose(nextResp)
			_ = s.store.UpdateProviderStatus(r.Context(), next.ID, domain.AccountCoolingDown, &nextAt)
			writeOpenAIError(w, http.StatusTooManyRequests, "all eligible accounts are rate limited", "subpool_rate_limited")
			return nil, false
		}
	}
	if nextResp.StatusCode >= 200 && nextResp.StatusCode < 300 {
		_ = s.store.MarkProviderSuccess(r.Context(), next.ID)
		_ = s.store.TouchAPIKey(r.Context(), route.Key.ID)
	}
	nextResp.Header.Set(accountHeader, next.ID)
	return nextResp, true
}

func (s *Server) recordRefreshFailure(ctx context.Context, accountID string, err error) bool {
	if codex.IsDefinitiveAuthError(err) {
		_ = s.store.UpdateProviderStatus(ctx, accountID, domain.AccountAuthFailed, nil)
		return true
	}
	retryAt := s.now().Add(time.Minute)
	_ = s.store.UpdateProviderStatus(ctx, accountID, domain.AccountCoolingDown, &retryAt)
	return false
}

func writeRefreshFailure(w http.ResponseWriter, definitive bool) {
	if definitive {
		writeOpenAIError(w, http.StatusUnauthorized, "provider authentication failed", "provider_authentication_error")
		return
	}
	writeOpenAIError(w, http.StatusServiceUnavailable, "provider credential refresh is temporarily unavailable", "provider_error")
}

func (s *Server) credentials(account domain.ProviderAccount) (codex.Credentials, error) {
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
func (s *Server) refresh(ctx context.Context, account domain.ProviderAccount, _ codex.Credentials) (domain.ProviderAccount, error) {
	return s.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
}

func accountHealthy(account domain.ProviderAccount, now time.Time) bool {
	switch account.Status {
	case domain.AccountActive:
		return true
	case domain.AccountCoolingDown:
		return account.CooldownUntil != nil && !now.Before(*account.CooldownUntil)
	default:
		return false
	}
}
func modelAllowed(model string, allowlist []string) bool {
	for _, allowed := range allowlist {
		if model == allowed {
			return true
		}
	}
	return false
}
func retryAfter(header http.Header, now time.Time) time.Time {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if parsed, err := http.ParseTime(value); err == nil {
		return parsed
	}
	return now.Add(time.Minute)
}
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type windowCounter struct {
	start time.Time
	count int
}
type rateLimiter struct {
	mu     sync.Mutex
	values map[string]windowCounter
}

func newRateLimiter() *rateLimiter { return &rateLimiter{values: make(map[string]windowCounter)} }
func (l *rateLimiter) Allow(key string, limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.values[key]
	if now.Sub(v.start) >= time.Minute || v.start.IsZero() {
		v = windowCounter{start: now}
	}
	if v.count >= limit {
		l.values[key] = v
		return false
	}
	v.count++
	l.values[key] = v
	return true
}

func copyResponseHeaders(dst, src http.Header) {
	for name, values := range src {
		switch strings.ToLower(name) {
		case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "content-length":
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}
func (s *Server) proxyUpstreamError(w http.ResponseWriter, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func (s *Server) proxyResponsesStream(w http.ResponseWriter, r *http.Request, keyID, poolID, accountID string, resp *http.Response) {
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(resp.StatusCode)
	responseID := ""
	fallbackEventHash := s.randomUsageEventHash()
	terminal := false
	input, output, err := copySSE(w, resp.Body, func(data []byte) {
		if id := responseIDFromEvent(data); id != "" {
			responseID = id
		}
		if event := eventType(data); event == "response.completed" || event == "response.incomplete" {
			terminal = true
		}
	})
	if err != nil || !terminal {
		return
	}
	if input > 0 || output > 0 {
		s.addUsage(keyID, s.usageEventHash(responseID, fallbackEventHash), input, output)
	}
	if responseID != "" && accountID != "" {
		s.saveSession(keyID, poolID, responseID, accountID)
	}
}

func (s *Server) proxyResponsesJSON(w http.ResponseWriter, r *http.Request, keyID, poolID, accountID string, resp *http.Response) {
	fallbackEventHash := s.randomUsageEventHash()
	value, input, output, _, err := completedResponse(resp)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "invalid provider response", "provider_error")
		return
	}
	responseID := ""
	if response, ok := value.(map[string]any); ok {
		responseID, _ = response["id"].(string)
		if responseID != "" && accountID != "" {
			s.saveSession(keyID, poolID, responseID, accountID)
		}
	}
	if input > 0 || output > 0 {
		s.addUsage(keyID, s.usageEventHash(responseID, fallbackEventHash), input, output)
	}
	writeJSON(w, http.StatusOK, value)
}

func copySSE(w http.ResponseWriter, reader io.Reader, observe func([]byte)) (int64, int64, error) {
	buffered := bufio.NewReaderSize(reader, 32<<10)
	var input, output int64
	for {
		line, err := buffered.ReadBytes('\n')
		if len(line) > 0 {
			if _, writeErr := w.Write(line); writeErr != nil {
				return input, output, writeErr
			}
			if data := sseData(line); len(data) > 0 {
				observe(data)
				i, o := usageFromEvent(data)
				if i > input {
					input = i
				}
				if o > output {
					output = o
				}
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return input, output, nil
			}
			return input, output, err
		}
	}
}

func sseData(line []byte) []byte {
	trimmed := strings.TrimSpace(string(line))
	if !strings.HasPrefix(trimmed, "data:") {
		return nil
	}
	return []byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
}

func completedResponse(resp *http.Response) (any, int64, int64, string, error) {
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "application/json") {
		var value any
		err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&value)
		raw, _ := json.Marshal(value)
		i, o := usageFromEvent(raw)
		status := ""
		if response, ok := value.(map[string]any); ok {
			status, _ = response["status"].(string)
		}
		if err == nil && status != "completed" && status != "incomplete" {
			err = fmt.Errorf("provider response is not terminal")
		}
		return value, i, o, status, err
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	var completed any
	status := ""
	var input, output int64
	for scanner.Scan() {
		data := sseData(scanner.Bytes())
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		i, o := usageFromEvent(data)
		if i > input {
			input = i
		}
		if o > output {
			output = o
		}
		var event struct {
			Type     string `json:"type"`
			Response any    `json:"response"`
		}
		if json.Unmarshal(data, &event) == nil && (event.Type == "response.completed" || event.Type == "response.incomplete") {
			completed = event.Response
			status = strings.TrimPrefix(event.Type, "response.")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, 0, "", err
	}
	if completed == nil {
		return nil, 0, 0, "", fmt.Errorf("provider stream did not contain a terminal response")
	}
	return completed, input, output, status, nil
}

func usageFromEvent(data []byte) (int64, int64) {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return 0, 0
	}
	usage, _ := value["usage"].(map[string]any)
	if response, ok := value["response"].(map[string]any); ok {
		if nested, ok := response["usage"].(map[string]any); ok {
			usage = nested
		}
	}
	if usage == nil {
		return 0, 0
	}
	return number(usage["input_tokens"]), number(usage["output_tokens"])
}

func eventType(data []byte) string {
	var value struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(data, &value)
	return value.Type
}

func responseIDFromEvent(data []byte) string {
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		return ""
	}
	if id, ok := value["response_id"].(string); ok {
		return id
	}
	if response, ok := value["response"].(map[string]any); ok {
		if id, ok := response["id"].(string); ok {
			return id
		}
	}
	return ""
}
func sessionHash(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
func (s *Server) addUsage(keyID string, eventHash []byte, input, output int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = s.store.AddUsage(ctx, keyID, eventHash, s.now(), input, output)
		if err == nil {
			return
		}
		if attempt < 3 && !waitRetry(ctx, time.Duration(attempt)*25*time.Millisecond) {
			break
		}
	}
	slog.Error("usage aggregation failed", "api_key_id", keyID, "attempts", 3, "error", err)
}

func (s *Server) usageEventHash(responseID string, fallback []byte) []byte {
	if responseID == "" {
		return fallback
	}
	return s.keys.Digest("usage-event:" + responseID)
}

func (s *Server) randomUsageEventHash() []byte {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err == nil {
		return s.keys.Digest("usage-event-fallback:" + string(random))
	}
	sequence := s.eventSeq.Add(1)
	return s.keys.Digest(fmt.Sprintf("usage-event-fallback:%d:%d", s.now().UnixNano(), sequence))
}
func (s *Server) saveSession(keyID, poolID, responseID, accountID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		err = s.store.SaveSessionBinding(ctx, keyID, poolID, sessionHash(responseID), accountID, s.now().Add(24*time.Hour))
		if err == nil {
			return
		}
		if attempt < 3 && !waitRetry(ctx, time.Duration(attempt)*25*time.Millisecond) {
			break
		}
	}
	slog.Error("session binding failed", "api_key_id", keyID, "attempts", 3, "error", err)
}

func waitRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func number(value any) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}
