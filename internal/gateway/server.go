package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/catalog"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"github.com/gesta-run/subpool/internal/store"
)

const (
	maxRequestBody      = 32 << 20
	maxProviderAttempts = 8
	accountHeader       = "X-Subpool-Internal-Account-Id"
	formatHeader        = "X-Subpool-Internal-Response-Format"
)

type retryReason string

const (
	retryUnavailable retryReason = "unavailable"
	retryAuth        retryReason = "authentication"
	retryRefresh     retryReason = "refresh"
	retryRateLimit   retryReason = "rate_limit"
)

type Cipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
}

type CodexClient interface {
	Responses(context.Context, []byte, http.Header, codex.Credentials) (*http.Response, error)
}

type OpenAICompatibleClient interface {
	Responses(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error)
	ChatCompletions(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error)
}

type Server struct {
	store      store.Store
	keys       *auth.APIKeys
	cipher     Cipher
	codex      CodexClient
	compatible OpenAICompatibleClient
	refresher  credential.AccountRefresher
	activity   *requestActivityThrottle
	catalog    *catalog.Service
	models     *modelCache
	now        func() time.Time
	eventSeq   atomic.Uint64
}

func New(st store.Store, keys *auth.APIKeys, cipher Cipher, client CodexClient, refresher credential.AccountRefresher, compatible ...OpenAICompatibleClient) *Server {
	var compatibleClient OpenAICompatibleClient
	if len(compatible) > 0 {
		compatibleClient = compatible[0]
	}
	return &Server{store: st, keys: keys, cipher: cipher, codex: client, compatible: compatibleClient, refresher: refresher,
		activity: newRequestActivityThrottle(), models: newModelCache(), now: time.Now}
}

func (s *Server) WithModelProviders(codexModels catalog.CodexModels, compatibleModels catalog.CompatibleModels) *Server {
	s.catalog = catalog.New(s.cipher, s.refresher, codexModels, compatibleModels)
	return s
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

type upstreamRequest struct {
	kind      string
	body      []byte
	codexBody []byte
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
	codexBody := forceStream(body)
	compatibleBody := forceProviderStream(body)
	resp, ok := s.call(w, r, route, upstreamRequest{kind: "responses", body: compatibleBody, codexBody: codexBody}, meta.PreviousResponseID)
	if !ok {
		return
	}
	defer resp.Body.Close()
	accountID := resp.Header.Get(accountHeader)
	resp.Header.Del(accountHeader)
	resp.Header.Del(formatHeader)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.proxyUpstreamError(w, resp)
		return
	}
	if meta.Stream {
		s.proxyResponsesStream(w, r, route.Key.ID, route.Pool.ID, accountID, meta.Model, resp)
		return
	}
	s.proxyResponsesJSON(w, r, route.Key.ID, route.Pool.ID, accountID, meta.Model, resp)
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
	responseBody, err := chatToResponses(body)
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error(), "invalid_request_error")
		return
	}
	resp, ok := s.call(w, r, route, upstreamRequest{kind: "chat_completions", body: body, codexBody: responseBody}, "")
	if !ok {
		return
	}
	defer resp.Body.Close()
	format := resp.Header.Get(formatHeader)
	resp.Header.Del(accountHeader)
	resp.Header.Del(formatHeader)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.proxyUpstreamError(w, resp)
		return
	}
	if meta.Stream {
		if format == "chat_completions" {
			s.proxyCompatibleChatStream(w, route.Key.ID, meta.Model, resp)
			return
		}
		s.proxyChatStream(w, r, route.Key.ID, meta.Model, resp)
		return
	}
	if format == "chat_completions" {
		s.proxyCompatibleChatJSON(w, route.Key.ID, meta.Model, resp)
		return
	}
	s.proxyChatJSON(w, r, route.Key.ID, meta.Model, resp)
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
	allowed, err := s.store.AllowAPIKeyRequest(r.Context(), route.Key.ID, route.Key.RateLimit, s.now())
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "rate limit state is unavailable", "server_error")
		return domain.KeyRoute{}, false
	}
	if !allowed {
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
	value["store"] = false
	for _, unsupported := range []string{"temperature", "top_p", "logprobs", "top_logprobs"} {
		delete(value, unsupported)
	}
	if instructions, ok := value["instructions"]; !ok || instructions == nil {
		value["instructions"] = ""
	}
	if input, ok := value["input"].(string); ok {
		value["input"] = []any{map[string]any{
			"role": "user",
			"content": []any{map[string]any{
				"type": "input_text",
				"text": input,
			}},
		}}
	}
	out, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return out
}

func forceProviderStream(body []byte) []byte {
	var value map[string]any
	if json.Unmarshal(body, &value) != nil {
		return body
	}
	value["stream"] = true
	out, err := json.Marshal(value)
	if err != nil {
		return body
	}
	return out
}

func (s *Server) call(w http.ResponseWriter, r *http.Request, route domain.KeyRoute, request upstreamRequest, previousResponseID string) (*http.Response, bool) {
	account := route.Account
	var err error
	continuation := previousResponseID != ""
	if continuation {
		account, err = s.store.ResolveSessionAccount(r.Context(), route.Key.ID, sessionHash(previousResponseID))
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable")
			return nil, false
		}
	}
	if !continuation && route.Pool.Provider == domain.ProviderMixed && account.CredentialType == domain.CredentialAPIKey {
		if preferred, reassignErr := s.store.ReassignAPIKey(r.Context(), route.Key.ID, route.Pool.ID, nil); reassignErr == nil {
			account = preferred
		}
	}
	attempted := make([]string, 0, maxProviderAttempts)
	lastRetry := retryUnavailable
	if (!continuation && !route.MembershipEnabled) || !accountHealthy(account, s.now()) {
		if continuation {
			writeOpenAIError(w, http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable")
			return nil, false
		}
		attempted = append(attempted, account.ID)
		account, err = s.store.ReassignAPIKey(r.Context(), route.Key.ID, route.Pool.ID, attempted)
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "no eligible account", "subpool_no_eligible_account")
			return nil, false
		}
	}
	for attempt := 0; attempt < maxProviderAttempts; attempt++ {
		attempted = append(attempted, account.ID)
		resp, retry, complete := s.attemptAccount(r, route, request, account)
		if complete {
			return resp, resp != nil
		}
		lastRetry = retry
		if continuation {
			writeRetryFailure(w, lastRetry)
			return nil, false
		}
		if attempt == maxProviderAttempts-1 {
			break
		}
		account, err = s.store.ReassignAPIKey(r.Context(), route.Key.ID, route.Pool.ID, attempted)
		if err != nil {
			writeRetryFailure(w, lastRetry)
			return nil, false
		}
	}
	writeRetryFailure(w, lastRetry)
	return nil, false
}

func (s *Server) attemptAccount(r *http.Request, route domain.KeyRoute, request upstreamRequest, account domain.ProviderAccount) (*http.Response, retryReason, bool) {
	credentials, err := s.credentials(account)
	if err != nil {
		s.recordHealthFailure(r.Context(), account.ID, "credential_unavailable")
		return nil, retryUnavailable, false
	}
	resp, err := s.providerAccountResponse(r.Context(), request, r.Header, credentials, account)
	if err != nil {
		s.recordHealthFailure(r.Context(), account.ID, "connection_failed")
		return nil, retryUnavailable, false
	}
	if !isAuthenticationStatus(resp.StatusCode) {
		return s.evaluateResponse(r.Context(), route.Key.ID, account.ID, resp)
	}
	drainAndClose(resp)
	if !refreshableCredentials(account) {
		_ = s.store.UpdateProviderStatus(r.Context(), account.ID, domain.AccountAuthFailed, nil)
		return nil, retryAuth, false
	}
	return s.retryRefreshedAccount(r, route, request, account)
}

func (s *Server) retryRefreshedAccount(r *http.Request, route domain.KeyRoute, request upstreamRequest, account domain.ProviderAccount) (*http.Response, retryReason, bool) {
	refreshed, err := s.refresh(r.Context(), account)
	if err != nil {
		if s.recordRefreshFailure(r.Context(), account.ID, err) {
			return nil, retryAuth, false
		}
		return nil, retryRefresh, false
	}
	credentials, err := s.credentials(refreshed)
	if err != nil {
		s.recordHealthFailure(r.Context(), refreshed.ID, "credential_unavailable")
		return nil, retryUnavailable, false
	}
	resp, err := s.providerAccountResponse(r.Context(), request, r.Header, credentials, refreshed)
	if err != nil {
		s.recordHealthFailure(r.Context(), refreshed.ID, "connection_failed")
		return nil, retryUnavailable, false
	}
	if isAuthenticationStatus(resp.StatusCode) {
		drainAndClose(resp)
		_ = s.store.UpdateProviderStatus(r.Context(), refreshed.ID, domain.AccountAuthFailed, nil)
		return nil, retryAuth, false
	}
	return s.evaluateResponse(r.Context(), route.Key.ID, refreshed.ID, resp)
}

func (s *Server) evaluateResponse(ctx context.Context, keyID, accountID string, resp *http.Response) (*http.Response, retryReason, bool) {
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAt := retryAfter(resp.Header, s.now())
		drainAndClose(resp)
		_ = s.store.UpdateProviderStatus(ctx, accountID, domain.AccountCoolingDown, &retryAt)
		return nil, retryRateLimit, false
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if s.activity.ShouldRecord(accountID, keyID, s.now()) {
			_ = s.store.RecordRequestSuccess(ctx, accountID, keyID, s.now())
		}
	}
	if resp.StatusCode >= 500 {
		s.recordHealthFailure(ctx, accountID, "provider_5xx")
		drainAndClose(resp)
		return nil, retryUnavailable, false
	}
	resp.Header.Set(accountHeader, accountID)
	return resp, "", true
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

func (s *Server) recordHealthFailure(ctx context.Context, accountID, code string) {
	now := s.now()
	_ = s.store.RecordProviderHealthFailure(ctx, accountID, code, now, now.Add(5*time.Minute))
}

func isAuthenticationStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

func writeRetryFailure(w http.ResponseWriter, reason retryReason) {
	switch reason {
	case retryAuth:
		writeOpenAIError(w, http.StatusUnauthorized, "provider authentication failed", "provider_authentication_error")
	case retryRefresh:
		writeOpenAIError(w, http.StatusServiceUnavailable, "provider credential refresh is temporarily unavailable", "provider_error")
	case retryRateLimit:
		writeOpenAIError(w, http.StatusTooManyRequests, "all eligible accounts are rate limited", "subpool_rate_limited")
	default:
		writeOpenAIError(w, http.StatusServiceUnavailable, "no eligible account", "subpool_no_eligible_account")
	}
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
func (s *Server) refresh(ctx context.Context, account domain.ProviderAccount) (domain.ProviderAccount, error) {
	return s.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
}

func refreshableCredentials(account domain.ProviderAccount) bool {
	return account.CredentialType == "" || account.CredentialType == domain.CredentialSubscription
}

func accountHealthy(account domain.ProviderAccount, now time.Time) bool {
	if account.HealthStatus == domain.HealthUnhealthy {
		return false
	}
	switch account.Status {
	case domain.AccountActive:
		return true
	case domain.AccountCoolingDown:
		return account.CooldownUntil != nil && !now.Before(*account.CooldownUntil)
	default:
		return false
	}
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

func (s *Server) providerAccountResponse(ctx context.Context, request upstreamRequest, header http.Header, codexCredentials codex.Credentials, account domain.ProviderAccount) (*http.Response, error) {
	var (
		resp           *http.Response
		err            error
		responseFormat = request.kind
	)
	switch account.Provider {
	case "", domain.ProviderCodex:
		resp, err = s.codex.Responses(ctx, request.codexBody, header, codexCredentials)
		responseFormat = "responses"
	case domain.ProviderOpenAICompatible:
		if s.compatible == nil {
			err = errors.New("OpenAI-compatible provider client is unavailable")
			break
		}
		plaintext, decryptErr := s.cipher.Decrypt(account.CredentialCiphertext)
		if decryptErr != nil {
			err = decryptErr
			break
		}
		var credentials openaicompat.Credentials
		if unmarshalErr := json.Unmarshal(plaintext, &credentials); unmarshalErr != nil {
			err = unmarshalErr
			break
		}
		if request.kind == "chat_completions" {
			resp, err = s.compatible.ChatCompletions(ctx, request.body, header, credentials)
		} else {
			resp, err = s.compatible.Responses(ctx, request.body, header, credentials)
		}
	default:
		err = fmt.Errorf("unsupported provider %q", account.Provider)
	}
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Body == nil {
		return nil, errors.New("provider returned an empty response")
	}
	resp.Header.Set(formatHeader, responseFormat)
	return resp, nil
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
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
