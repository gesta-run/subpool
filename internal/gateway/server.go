package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gesta-run/subpool/internal/adapter"
	"github.com/gesta-run/subpool/internal/adapter/antigravitywire"
	"github.com/gesta-run/subpool/internal/adapter/copilotcompact"
	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/catalog"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/gateway/clientquota"
	"github.com/gesta-run/subpool/internal/gateway/responsesws"
	"github.com/gesta-run/subpool/internal/gateway/routingstats"
	"github.com/gesta-run/subpool/internal/protocol/responseevent"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/copilot"
	"github.com/gesta-run/subpool/internal/store"
)

const (
	maxProviderAttempts = 8
	accountHeader       = "X-Subpool-Internal-Account-Id"
	// Retry-After can point at the next quota window, days away, while quota may
	// return sooner (manual reset, top-up); static keys have no quota probe.
	maxRateLimitCooldown = 15 * time.Minute
)

type retryReason string

const (
	retryUnavailable retryReason = "unavailable"
	retryAuth        retryReason = "authentication"
	retryRefresh     retryReason = "refresh"
	retryRateLimit   retryReason = "rate_limit"
	retryInvalid     retryReason = "invalid_request"
	retryTransport   retryReason = "transport"
	retryTimeout     retryReason = "timeout"
	retryCanceled    retryReason = "canceled"
	retryProvider5xx retryReason = "provider_5xx"
	retryUnsupported retryReason = "unsupported"
)

type Cipher interface {
	Encrypt([]byte) ([]byte, error)
	Decrypt([]byte) ([]byte, error)
	copilotcompact.ScopedCipher
}

type CodexClient = adapter.CodexClient

type OpenAICompatibleClient = adapter.CompatibleClient

type CopilotClient = adapter.CopilotClient

type Server struct {
	store                 store.Store
	keys                  *auth.APIKeys
	cipher                Cipher
	codex                 CodexClient
	compatible            OpenAICompatibleClient
	copilot               CopilotClient
	antigravity           adapter.AntigravityClient
	antigravitySignatures *antigravitywire.Signatures
	antigravityLimits     *antigravitywire.ModelLimits
	refresher             credential.AccountRefresher
	activity              *requestActivityThrottle
	catalog               *catalog.Service
	models                *modelCache
	responsesWS           *responsesws.Hub
	maxRequestBodyBytes   int64
	requestBodyBudget     *byteBudget
	requestBodyTimeout    time.Duration
	now                   func() time.Time
	eventSeq              atomic.Uint64
	routingStats          routingstats.Counters
}

func New(st store.Store, keys *auth.APIKeys, cipher Cipher, client CodexClient, refresher credential.AccountRefresher, compatible ...OpenAICompatibleClient) *Server {
	var compatibleClient OpenAICompatibleClient
	if len(compatible) > 0 {
		compatibleClient = compatible[0]
	}
	server := &Server{store: st, keys: keys, cipher: cipher, codex: client, compatible: compatibleClient, refresher: refresher,
		activity: newRequestActivityThrottle(), models: newModelCache(), maxRequestBodyBytes: defaultMaxRequestBodyBytes,
		requestBodyBudget: newByteBudget(defaultMaxInflightRequestBodyBytes), requestBodyTimeout: defaultRequestBodyReadTimeout, now: time.Now}
	server.responsesWS = responsesws.New(responsesWSBackend{server: server})
	return server
}

func (s *Server) WithCopilot(client CopilotClient) *Server {
	s.copilot = client
	return s
}

func (s *Server) WithAntigravity(client adapter.AntigravityClient) *Server {
	s.antigravity = client
	s.antigravitySignatures = antigravitywire.NewSignatures()
	if storage, ok := s.store.(antigravitywire.SignatureStorage); ok {
		s.antigravitySignatures.WithStorage(storage, s.cipher)
	}
	s.antigravityLimits = antigravitywire.NewModelLimits()
	return s
}

func (s *Server) WithAntigravityModels(client catalog.AntigravityModels) *Server {
	s.catalog.WithAntigravity(client)
	return s
}

func (s *Server) WithRequestBodyLimits(maxRequestBodyBytes, maxInflightRequestBodyBytes int64, readTimeout time.Duration) *Server {
	minimumBudget, valid := requestBodyCost(maxRequestBodyBytes, maxHTTPRequestBodyCopies)
	if maxRequestBodyBytes <= 0 || !valid || maxInflightRequestBodyBytes < minimumBudget || readTimeout <= 0 {
		panic("invalid request body limits")
	}
	s.maxRequestBodyBytes = maxRequestBodyBytes
	s.requestBodyBudget = newByteBudget(maxInflightRequestBodyBytes)
	s.requestBodyTimeout = readTimeout
	return s
}

func (s *Server) WithModelProviders(codexModels catalog.CodexModels, compatibleModels catalog.CompatibleModels, copilotModels ...catalog.CopilotModels) *Server {
	s.catalog = catalog.New(s.cipher, s.refresher, codexModels, compatibleModels, copilotModels...)
	return s
}

func (s *Server) WithResponsesWebSocket(enabled, forceHTTPBridge bool, codexUpstreamURL string) *Server {
	s.responsesWS.Configure(enabled, forceHTTPBridge, codexUpstreamURL)
	return s
}

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/responses", s.handleResponses)
	mux.HandleFunc("POST /v1/responses/compact", s.handleCompact)
	if s.responsesWS.Enabled() {
		mux.HandleFunc("GET /v1/responses", s.responsesWS.Handle)
	}
	mux.HandleFunc("POST /v1/chat/completions", s.handleChat)
	mux.HandleFunc("POST /v1/images/generations", s.handleImageGenerations)
	mux.HandleFunc("POST /v1/images/edits", s.handleImageEdits)
	mux.HandleFunc("GET /v1/models", s.handleModels)
}

func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	route, ok := s.authorize(w, r, "responses")
	if !ok {
		return
	}
	body, meta, reservation, ok := s.readReservedRequest(w, r)
	if !ok {
		return
	}
	defer reservation.release()
	if meta.Stream || meta.PreviousResponseID != "" {
		writeOpenAIError(w, http.StatusBadRequest, "standalone compaction requires full input and does not support streaming or previous_response_id", "invalid_request_error")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), compactBodyReservationKey{}, reservation))
	response, ok := s.call(w, r, route, adapter.Request{Kind: "responses_compact", Model: meta.Model, Body: body}, "")
	reservation.release()
	if !ok {
		return
	}
	defer response.Body.Close()
	accountID := response.Header.Get(accountHeader)
	response.Header.Del(accountHeader)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.proxyUpstreamError(w, response.Response)
		return
	}
	clientquota.MoveHeaders(w.Header(), response.Header)
	response.Render(w, false, adapter.Request{Kind: "responses_compact", Model: meta.Model}, s.responseObserver(route.Key.ID, "", accountID))
}

type requestMeta struct {
	Model              string `json:"model"`
	Stream             bool   `json:"stream"`
	PreviousResponseID string `json:"previous_response_id"`
	StreamOptions      struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
}

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	route, ok := s.authorize(w, r, "responses")
	if !ok {
		return
	}
	body, meta, reservation, ok := s.readReservedRequest(w, r)
	if !ok {
		return
	}
	releaseBody := reservation.release
	defer releaseBody()
	r = r.WithContext(context.WithValue(r.Context(), compactBodyReservationKey{}, reservation))
	response, ok := s.call(w, r, route, adapter.Request{Kind: "responses", Model: meta.Model, Body: body}, meta.PreviousResponseID)
	if !response.RetainsRequestBody() {
		releaseBody()
	}
	if !ok {
		return
	}
	defer response.Body.Close()
	accountID := response.Header.Get(accountHeader)
	response.Header.Del(accountHeader)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.proxyUpstreamError(w, response.Response)
		return
	}
	clientquota.MoveHeaders(w.Header(), response.Header)
	response.Render(w, meta.Stream, adapter.Request{Kind: "responses", Model: meta.Model},
		s.responseObserver(route.Key.ID, route.Pool.ID, accountID))
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	route, ok := s.authorize(w, r, "chat_completions")
	if !ok {
		return
	}
	body, meta, releaseBody, ok := s.readRequest(w, r)
	if !ok {
		return
	}
	defer releaseBody()
	response, ok := s.call(w, r, route, adapter.Request{Kind: "chat_completions", Model: meta.Model, Body: body}, "")
	releaseBody()
	if !ok {
		return
	}
	defer response.Body.Close()
	accountID := response.Header.Get(accountHeader)
	response.Header.Del(accountHeader)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		s.proxyUpstreamError(w, response.Response)
		return
	}
	response.Render(w, meta.Stream, adapter.Request{Kind: "chat_completions", Model: meta.Model, IncludeUsage: meta.StreamOptions.IncludeUsage},
		s.responseObserver(route.Key.ID, "", accountID))
}

func (s *Server) handleImageGenerations(w http.ResponseWriter, r *http.Request) {
	s.handleImages(w, r, "image_generations")
}

func (s *Server) handleImageEdits(w http.ResponseWriter, r *http.Request) {
	s.handleImages(w, r, "image_edits")
}

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request, kind string) {
	// Image generation is a Codex capability, so existing Responses-scoped keys
	// remain valid when the Codex client invokes its built-in image tool.
	route, ok := s.authorize(w, r, "responses")
	if !ok {
		return
	}
	body, meta, releaseBody, ok := s.readRequest(w, r)
	if !ok {
		return
	}
	defer releaseBody()
	resp, ok := s.call(w, r, route, adapter.Request{Kind: kind, Model: meta.Model, Body: body}, "")
	releaseBody()
	if !ok {
		return
	}
	defer resp.Body.Close()
	accountID := resp.Header.Get(accountHeader)
	resp.Header.Del(accountHeader)
	s.proxyImageJSON(w, route.Key.ID, accountID, meta.Model, resp.Response)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request, requiredScope string) (domain.KeyRoute, bool) {
	route, requestErr := s.authenticate(r.Context(), r.Header.Get("Authorization"), requiredScope)
	if requestErr != nil {
		writeOpenAIError(w, requestErr.status, requestErr.message, requestErr.code)
		return domain.KeyRoute{}, false
	}
	if requestErr = s.allowAPIKeyRequest(r.Context(), route); requestErr != nil {
		writeOpenAIError(w, requestErr.status, requestErr.message, requestErr.code)
		return domain.KeyRoute{}, false
	}
	return route, true
}

type gatewayError struct {
	status  int
	message string
	code    string
}

func (s *Server) authenticate(ctx context.Context, authorization, requiredScope string) (domain.KeyRoute, *gatewayError) {
	plain, err := auth.Bearer(authorization)
	if err != nil {
		return domain.KeyRoute{}, &gatewayError{http.StatusUnauthorized, "invalid API key", "invalid_api_key"}
	}
	route, err := s.store.ResolveAPIKey(ctx, s.keys.Digest(plain))
	if err != nil {
		return domain.KeyRoute{}, &gatewayError{http.StatusUnauthorized, "invalid API key", "invalid_api_key"}
	}
	if !scopeAllowed(route.Key.Scopes, requiredScope) {
		return domain.KeyRoute{}, &gatewayError{http.StatusForbidden, "API key scope does not allow this endpoint", "insufficient_scope"}
	}
	return route, nil
}

func (s *Server) allowAPIKeyRequest(ctx context.Context, route domain.KeyRoute) *gatewayError {
	allowed, err := s.store.AllowAPIKeyRequest(ctx, route.Key.ID, route.Key.RateLimit, s.now())
	if err != nil {
		return &gatewayError{http.StatusServiceUnavailable, "rate limit state is unavailable", "server_error"}
	}
	if !allowed {
		return &gatewayError{http.StatusTooManyRequests, "API key rate limit exceeded", "subpool_rate_limited"}
	}
	return nil
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

func (s *Server) call(w http.ResponseWriter, r *http.Request, route domain.KeyRoute, request adapter.Request, previousResponseID string) (*adapter.Response, bool) {
	if strings.HasPrefix(previousResponseID, "resp_subpool_cmp_") {
		writeOpenAIError(w, http.StatusBadRequest, "Subpool compaction responses do not store upstream history; replay the compaction item and full remaining input", "previous_response_not_found")
		return nil, false
	}
	account := route.Account
	var err error
	continuation := previousResponseID != ""
	if continuation {
		account, err = s.store.ResolveSessionAccount(r.Context(), route.Key.ID, responseevent.SessionHash(previousResponseID))
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable")
			return nil, false
		}
	}
	if !continuation && !isImageRequest(request.Kind) && (route.Pool.Provider == domain.ProviderMixed || route.Pool.Provider == domain.ProviderOpenAICompatible) && account.CredentialType == domain.CredentialAPIKey {
		if preferred, reassignErr := s.store.ReassignAPIKey(r.Context(), route.Key.ID, route.Pool.ID, nil); reassignErr == nil {
			account = preferred
		}
	}
	if !continuation && !isImageRequest(request.Kind) && route.MembershipEnabled && route.Account.Provider == domain.ProviderCodex && accountHealthy(account, s.now()) {
		if preferred, quotaErr := s.store.PreferQuotaAccount(r.Context(), route); quotaErr == nil {
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
	// Observe once per client request, before retries. Image requests are outside
	// the Codex conversation-routing population.
	if account.Provider == domain.ProviderCodex && !isImageRequest(request.Kind) {
		s.routingStats.Record(continuation, false)
	}
	for attempt := 0; attempt < maxProviderAttempts; attempt++ {
		attempted = append(attempted, account.ID)
		resp, retry, complete := s.attemptAccount(r, route, request, account)
		if complete {
			return resp, resp != nil
		}
		if retry == retryInvalid {
			writeOpenAIError(w, http.StatusBadRequest, "client_metadata must be an object", "invalid_request_error")
			return nil, false
		}
		if retry == retryTimeout {
			writeRetryFailure(w, retry)
			return nil, false
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

func (s *Server) attemptAccount(r *http.Request, route domain.KeyRoute, request adapter.Request, account domain.ProviderAccount) (*adapter.Response, retryReason, bool) {
	if isImageRequest(request.Kind) && !isCodexSubscriptionAccount(account) {
		return nil, retryUnsupported, false
	}
	credentials, err := s.credentials(account)
	if err != nil {
		s.recordHealthFailure(r.Context(), account.ID, "credential_unavailable")
		return nil, retryUnavailable, false
	}
	if account.Provider == domain.ProviderAntigravity && time.Until(credentials.Antigravity.ExpiresAt) <= time.Minute {
		return s.retryRefreshedAccount(r, route, request, account)
	}
	response, err := s.providerAccountResponse(r.Context(), request, r.Header, credentials, account, route.Pool.Provider, route.Key.ID)
	if err != nil {
		if failure := terminalAdapterFailure(err); failure != nil {
			return failure, "", true
		}
		if errors.Is(err, adapter.ErrUnsupported) {
			return nil, retryUnsupported, false
		}
		var requestError *adapter.RequestError
		if errors.As(err, &requestError) {
			if route.Pool.Provider == domain.ProviderMixed {
				return nil, retryUnsupported, false
			}
			return &adapter.Response{Response: openAIErrorResponse(http.StatusBadRequest, requestError.Message, requestError.Code)}, "", true
		}
		retry, complete := s.evaluateProviderError(r.Context(), account.ID, err)
		return nil, retry, complete
	}
	if !isAuthenticationStatus(account.Provider, response.StatusCode) {
		return s.evaluateResponse(r.Context(), route.Key.ID, account.ID, response)
	}
	drainAndClose(response.Response)
	if !refreshableCredentials(account) {
		_ = s.store.UpdateProviderStatus(r.Context(), account.ID, domain.AccountAuthFailed, nil)
		return nil, retryAuth, false
	}
	return s.retryRefreshedAccount(r, route, request, account)
}

func (s *Server) retryRefreshedAccount(r *http.Request, route domain.KeyRoute, request adapter.Request, account domain.ProviderAccount) (*adapter.Response, retryReason, bool) {
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
	resp, err := s.providerAccountResponse(r.Context(), request, r.Header, credentials, refreshed, route.Pool.Provider, route.Key.ID)
	if err != nil {
		if failure := terminalAdapterFailure(err); failure != nil {
			return failure, "", true
		}
		var requestError *adapter.RequestError
		if errors.As(err, &requestError) {
			if route.Pool.Provider == domain.ProviderMixed {
				return nil, retryUnsupported, false
			}
			return &adapter.Response{Response: openAIErrorResponse(http.StatusBadRequest, requestError.Message, requestError.Code)}, "", true
		}
		if errors.Is(err, adapter.ErrUnsupported) {
			return nil, retryUnsupported, false
		}
		retry, complete := s.evaluateProviderError(r.Context(), refreshed.ID, err)
		return nil, retry, complete
	}
	if isAuthenticationStatus(refreshed.Provider, resp.StatusCode) {
		drainAndClose(resp.Response)
		_ = s.store.UpdateProviderStatus(r.Context(), refreshed.ID, domain.AccountAuthFailed, nil)
		return nil, retryAuth, false
	}
	return s.evaluateResponse(r.Context(), route.Key.ID, refreshed.ID, resp)
}

func (s *Server) evaluateResponse(ctx context.Context, keyID, accountID string, resp *adapter.Response) (*adapter.Response, retryReason, bool) {
	if resp.StatusCode == http.StatusTooManyRequests || resp.ModelScopedLimit() {
		retryAt := retryAfter(resp.Header, s.now())
		modelScoped := resp.ModelScopedLimit()
		drainAndClose(resp.Response)
		if !modelScoped {
			_ = s.store.UpdateProviderStatus(ctx, accountID, domain.AccountCoolingDown, &retryAt)
		}
		return nil, retryRateLimit, false
	}
	if unsupportedModelResponse(resp.Response) {
		drainAndClose(resp.Response)
		return nil, retryUnsupported, false
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if s.activity.ShouldRecord(accountID, keyID, s.now()) {
			if err := s.store.RecordRequestSuccess(ctx, accountID, keyID, s.now()); err != nil {
				slog.Error("request activity update failed", "account_id", accountID, "api_key_id", keyID, "error", err)
			}
		}
	}
	if resp.StatusCode >= 500 {
		s.recordHealthFailure(ctx, accountID, "provider_5xx")
		drainAndClose(resp.Response)
		return nil, retryProvider5xx, false
	}
	resp.Header.Set(accountHeader, accountID)
	return resp, "", true
}

func (s *Server) recordRefreshFailure(ctx context.Context, accountID string, err error) bool {
	if codex.IsDefinitiveAuthError(err) || antigravity.IsDefinitiveAuthError(err) {
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

func (s *Server) recordProviderStreamLimit(accountID string, limit responseevent.LimitKind) {
	if accountID == "" || limit == responseevent.LimitNone {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var err error
	if limit == responseevent.LimitQuota {
		err = s.store.SetProviderUsageAllowed(ctx, accountID, false)
	} else {
		retryAt := s.now().Add(time.Minute)
		err = s.store.UpdateProviderStatus(ctx, accountID, domain.AccountCoolingDown, &retryAt)
	}
	if err != nil {
		slog.Error("provider stream limit update failed", "account_id", accountID, "limit_kind", limit, "error", err)
	}
}

func isAuthenticationStatus(provider string, status int) bool {
	if provider == domain.ProviderCopilot || provider == domain.ProviderAntigravity {
		return status == http.StatusUnauthorized
	}
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

func (s *Server) evaluateProviderError(ctx context.Context, accountID string, err error) (retryReason, bool) {
	if ctx.Err() != nil {
		return retryCanceled, true
	}
	if errors.Is(err, codex.ErrInvalidClientMetadata) {
		return retryInvalid, false
	}
	if copilot.IsDefinitiveAuthError(err) {
		_ = s.store.UpdateProviderStatus(ctx, accountID, domain.AccountAuthFailed, nil)
		return retryAuth, false
	}
	var copilotError *copilot.HTTPError
	if errors.As(err, &copilotError) {
		switch {
		case copilotError.StatusCode == http.StatusTooManyRequests:
			header := make(http.Header)
			header.Set("Retry-After", copilotError.RetryAfter)
			retryAt := retryAfter(header, s.now())
			_ = s.store.UpdateProviderStatus(ctx, accountID, domain.AccountCoolingDown, &retryAt)
			return retryRateLimit, false
		case copilotError.StatusCode >= http.StatusInternalServerError:
			s.recordHealthFailure(ctx, accountID, "provider_5xx")
			return retryProvider5xx, false
		}
	}
	retry, healthCode := classifyProviderError(err)
	slog.Warn("provider request failed", "account_id", accountID, "reason", retry, "error", err)
	if healthCode != "" {
		s.recordHealthFailure(ctx, accountID, healthCode)
	}
	return retry, false
}

func classifyProviderError(err error) (retryReason, string) {
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return retryTimeout, ""
	}
	return retryTransport, "connection_failed"
}

func writeRetryFailure(w http.ResponseWriter, reason retryReason) {
	switch reason {
	case retryAuth:
		writeOpenAIError(w, http.StatusUnauthorized, "provider authentication failed", "provider_authentication_error")
	case retryRefresh:
		writeOpenAIError(w, http.StatusServiceUnavailable, "provider credential refresh is temporarily unavailable", "provider_error")
	case retryRateLimit:
		writeOpenAIError(w, http.StatusTooManyRequests, "all eligible accounts are rate limited", "subpool_rate_limited")
	case retryTimeout:
		writeOpenAIError(w, http.StatusGatewayTimeout, "provider response timed out", "provider_timeout")
	case retryUnsupported:
		writeOpenAIError(w, http.StatusBadRequest, "no eligible account supports the requested model or endpoint", "unsupported_provider_endpoint")
	default:
		writeOpenAIError(w, http.StatusServiceUnavailable, "no eligible account", "subpool_no_eligible_account")
	}
}

type replayReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *replayReadCloser) Close() error {
	return r.closer.Close()
}

func unsupportedModelResponse(resp *http.Response) bool {
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		return false
	}
	original := resp.Body
	prefix, err := io.ReadAll(io.LimitReader(original, 64<<10))
	resp.Body = &replayReadCloser{Reader: io.MultiReader(bytes.NewReader(prefix), original), closer: original}
	if err != nil {
		return false
	}
	var envelope struct {
		Error struct {
			Code    any    `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(prefix, &envelope) != nil {
		return false
	}
	for _, signal := range []string{fmt.Sprint(envelope.Error.Code), envelope.Error.Type} {
		normalized := strings.ToLower(strings.ReplaceAll(signal, "-", "_"))
		for _, candidate := range []string{"model_not_found", "unsupported_model", "model_not_supported", "model_unavailable"} {
			if normalized == candidate {
				return true
			}
		}
	}
	message := strings.ToLower(envelope.Error.Message)
	for _, phrase := range []string{"model is not available", "model not found", "unsupported model", "model is not supported", "model does not support this endpoint"} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return strings.Contains(message, "model") && strings.Contains(message, "not available for this endpoint")
}

func (s *Server) credentials(account domain.ProviderAccount) (adapter.Credentials, error) {
	plaintext, err := s.cipher.Decrypt(account.CredentialCiphertext)
	if err != nil {
		return adapter.Credentials{}, err
	}
	var credentials adapter.Credentials
	switch account.Provider {
	case "", domain.ProviderCodex:
		err = json.Unmarshal(plaintext, &credentials.Codex)
	case domain.ProviderOpenAICompatible:
		err = json.Unmarshal(plaintext, &credentials.Compatible)
	case domain.ProviderCopilot:
		err = json.Unmarshal(plaintext, &credentials.Copilot)
	case domain.ProviderAntigravity:
		err = json.Unmarshal(plaintext, &credentials.Antigravity)
	default:
		err = fmt.Errorf("unsupported provider %q", account.Provider)
	}
	if err != nil {
		return adapter.Credentials{}, err
	}
	return credentials, nil
}
func (s *Server) refresh(ctx context.Context, account domain.ProviderAccount) (domain.ProviderAccount, error) {
	return s.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
}

func refreshableCredentials(account domain.ProviderAccount) bool {
	return isCodexSubscriptionAccount(account) || account.Provider == domain.ProviderAntigravity && account.CredentialType == domain.CredentialSubscription
}

func isCodexSubscriptionAccount(account domain.ProviderAccount) bool {
	return (account.Provider == "" || account.Provider == domain.ProviderCodex) &&
		(account.CredentialType == "" || account.CredentialType == domain.CredentialSubscription)
}

func isImageRequest(kind string) bool {
	return kind == "image_generations" || kind == "image_edits"
}

func accountHealthy(account domain.ProviderAccount, now time.Time) bool {
	if account.HealthStatus == domain.HealthUnhealthy {
		return false
	}
	if len(account.QuotaSnapshot) > 0 {
		var quota codex.UsageSnapshot
		if json.Unmarshal(account.QuotaSnapshot, &quota) == nil && !quota.AllowsUsageAt(now) {
			return false
		}
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
	retryAt := now.Add(time.Minute)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		retryAt = now.Add(time.Duration(seconds) * time.Second)
	} else if parsed, err := http.ParseTime(value); err == nil {
		retryAt = parsed
	}
	if limit := now.Add(maxRateLimitCooldown); retryAt.After(limit) {
		slog.Warn("provider Retry-After capped", "retry_after", value, "requested_until", retryAt, "capped_until", limit)
		return limit
	}
	return retryAt
}
func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
}

func (s *Server) providerAccountResponse(ctx context.Context, request adapter.Request, header http.Header, credentials adapter.Credentials, account domain.ProviderAccount, poolProvider, keyID string) (*adapter.Response, error) {
	var release func()
	defer func() {
		if release != nil {
			release()
		}
	}()
	trigger, opaque, own := copilotcompact.Features(request.Body)
	if own || account.Provider == domain.ProviderCopilot && (request.Kind == "responses_compact" || request.Kind == "responses" && (trigger || opaque)) {
		var err error
		release, err = s.reserveCompactionTranscript(ctx, len(request.Body))
		if err != nil {
			return nil, err
		}
	}
	provider, err := adapter.For(account.Provider, adapter.Clients{Codex: s.codex, Compatible: s.compatible, Copilot: s.copilot, Antigravity: s.antigravity, AntigravitySignatures: s.antigravitySignatures, AntigravityLimits: s.antigravityLimits})
	if err != nil {
		return nil, err
	}
	response, err := provider.RoundTrip(ctx, request,
		header, credentials, adapter.Options{AccountID: account.ID, FastMode: account.FastModeEnabled, AllowToolFallback: poolProvider == domain.ProviderCopilot,
			CompactionState: &copilotcompact.State{Cipher: s.cipher, Scope: keyID},
			InternalUsage:   func(input, output int64) { s.addAccountUsage(keyID, account.ID, "", request.Model, input, output) }})
	if err != nil {
		return nil, err
	}
	if response == nil || response.Response == nil || response.Body == nil {
		return nil, errors.New("provider returned an empty response")
	}
	if response.RetainsRequestBody() && release != nil {
		response.Body = &compactResponseBody{ReadCloser: response.Body, release: sync.OnceFunc(release)}
		release = nil
	}
	if response.Header == nil {
		response.Header = make(http.Header)
	}
	clientquota.ApplyHeaders(response.Header, account, s.now())
	return response, nil
}

func terminalAdapterFailure(err error) *adapter.Response {
	if errors.Is(err, errRequestBodyCapacity) {
		response := openAIErrorResponse(http.StatusServiceUnavailable, "request body capacity is temporarily unavailable", "subpool_capacity_exceeded")
		response.Header.Set("Retry-After", "1")
		response.Header.Set("X-Subpool-Error-Source", "subpool")
		return &adapter.Response{Response: response}
	}
	var failure *adapter.ResultError
	if errors.As(err, &failure) {
		return &adapter.Response{Response: openAIErrorResponse(http.StatusBadGateway, failure.Message, failure.Code)}
	}
	return nil
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}

func openAIErrorResponse(status int, message, code string) *http.Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
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
