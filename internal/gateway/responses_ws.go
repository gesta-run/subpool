package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/store"
)

const (
	responsesWSMaxConnections        = 256
	responsesWSMaxConnectionsPerKey  = 8
	responsesWSMaxTurnsPerConnection = 16
	responsesWSMaxTurnsPerAccount    = 16
	responsesWSMaxNamedStreams       = 32
	responsesWSMaxRememberedIDs      = 128
	responsesWSFirstMessageTimeout   = 30 * time.Second
	responsesWSIdleTimeout           = 5 * time.Minute
	responsesWSLifetime              = 55 * time.Minute
	responsesWSWriteTimeout          = 30 * time.Second
	responsesWSDialTimeout           = 10 * time.Second
)

type responsesWSSession struct {
	hub           *responsesWSHub
	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	keyDigest     []byte
	keyID         string
	poolID        string
	headers       http.Header
	account       domain.ProviderAccount
	upstream      *websocket.Conn
	upstreamModel string
	native        bool
	pinned        bool
	lastActive    atomic.Int64

	mu        sync.Mutex
	writeMu   sync.Mutex
	closeOnce sync.Once
	workers   sync.WaitGroup
	closed    bool
	done      chan struct{}
	streams   map[string]*responsesWSStream
	named     map[string]struct{}
	responses map[string]struct{}
	pending   int
}

type responsesWSStream struct {
	turns         []*responsesWSTurn
	bridgeRunning bool
}

type responsesWSTurn struct {
	streamID  string
	accountID string
	model     string
	payload   []byte
	response  string
	input     int64
	output    int64
	terminal  string
	finished  bool
}

type responsesWSRequest struct {
	Type               string  `json:"type"`
	StreamID           *string `json:"stream_id"`
	Model              string  `json:"model"`
	PreviousResponseID string  `json:"previous_response_id"`
	raw                []byte
}

type responsesWSEvent struct {
	Type     string `json:"type"`
	StreamID string `json:"stream_id"`
}

func (s *responsesWSSession) readClient() {
	first := true
	for {
		readCtx := s.ctx
		var cancel context.CancelFunc
		if first {
			readCtx, cancel = context.WithTimeout(s.ctx, responsesWSFirstMessageTimeout)
		}
		messageType, payload, err := s.conn.Read(readCtx)
		if cancel != nil {
			cancel()
		}
		if err != nil {
			return
		}
		if messageType != websocket.MessageText {
			s.close(websocket.StatusUnsupportedData, "text messages required")
			return
		}
		first = false
		s.touch()
		request, requestErr := parseResponsesWSRequest(payload)
		if requestErr != nil {
			s.sendError("", requestErr.code, requestErr.message)
			continue
		}
		if !s.handleRequest(request) {
			return
		}
	}
}

func (s *responsesWSSession) handleRequest(request responsesWSRequest) bool {
	route, requestErr := s.resolveRoute()
	if requestErr != nil {
		s.sendError(request.streamID(), requestErr.code, requestErr.message)
		if requestErr.code == "invalid_api_key" {
			s.close(websocket.StatusPolicyViolation, "invalid API key")
			return false
		}
		return true
	}
	if requestErr = s.hub.server.allowAPIKeyRequest(s.ctx, route); requestErr != nil {
		s.hub.turnsRejected.Add(1)
		s.sendError(request.streamID(), requestErr.code, requestErr.message)
		return true
	}
	firstBridge, bridgeCanFailover, requestErr := s.ensurePinned(request, route)
	if requestErr != nil {
		s.hub.turnsRejected.Add(1)
		s.sendError(request.streamID(), requestErr.code, requestErr.message)
		if requestErr.code == "subpool_websocket_model_changed" {
			s.close(websocket.StatusPolicyViolation, "model changed; reconnect required")
			return false
		}
		return true
	}
	if requestErr = s.validateContinuation(request.PreviousResponseID); requestErr != nil {
		s.hub.turnsRejected.Add(1)
		s.sendError(request.streamID(), requestErr.code, requestErr.message)
		return true
	}
	payload, requestErr := s.preparePayload(request)
	if requestErr != nil {
		s.hub.turnsRejected.Add(1)
		s.sendError(request.streamID(), requestErr.code, requestErr.message)
		return true
	}
	turn, requestErr := s.reserveTurn(request, payload)
	if requestErr != nil {
		s.hub.turnsRejected.Add(1)
		s.sendError(request.streamID(), requestErr.code, requestErr.message)
		return true
	}
	s.hub.turnsTotal.Add(1)
	s.mu.Lock()
	native := s.native
	s.mu.Unlock()
	if native {
		if err := s.writeUpstream(payload); err != nil {
			s.finishTurn(turn)
			s.close(websocket.StatusInternalError, "upstream write failed")
			return false
		}
		return true
	}
	if firstBridge {
		s.startInitialBridge(turn, route, bridgeCanFailover)
		return true
	}
	s.startBridge(request.streamID(), nil)
	return true
}

func (s *responsesWSSession) resolveRoute() (domain.KeyRoute, *gatewayError) {
	s.mu.Lock()
	pinned := s.pinned
	accountID := s.account.ID
	s.mu.Unlock()
	var (
		route domain.KeyRoute
		err   error
	)
	if pinned {
		route, err = s.hub.server.store.ResolvePinnedAPIKey(s.ctx, s.keyDigest, s.poolID, accountID)
	} else {
		route, err = s.hub.server.store.ResolveAPIKey(s.ctx, s.keyDigest)
	}
	if err != nil {
		if errors.Is(err, store.ErrPinnedUnavailable) {
			return domain.KeyRoute{}, &gatewayError{http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable"}
		}
		if errors.Is(err, store.ErrNotFound) {
			return domain.KeyRoute{}, &gatewayError{http.StatusUnauthorized, "invalid API key", "invalid_api_key"}
		}
		return domain.KeyRoute{}, &gatewayError{http.StatusServiceUnavailable, "API key state is unavailable", "server_error"}
	}
	if !scopeAllowed(route.Key.Scopes, "responses") {
		return domain.KeyRoute{}, &gatewayError{http.StatusForbidden, "API key scope does not allow this endpoint", "insufficient_scope"}
	}
	if pinned {
		if !route.MembershipEnabled || !accountHealthy(route.Account, s.hub.server.now()) {
			return domain.KeyRoute{}, &gatewayError{http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable"}
		}
		s.mu.Lock()
		s.account = route.Account
		s.mu.Unlock()
	}
	return route, nil
}

func (s *responsesWSSession) ensurePinned(request responsesWSRequest, route domain.KeyRoute) (bool, bool, *gatewayError) {
	s.mu.Lock()
	if s.pinned {
		native := s.native
		upstreamModel := s.upstreamModel
		s.mu.Unlock()
		if native && request.Model != upstreamModel {
			return false, false, &gatewayError{http.StatusConflict, "model changed; reconnect to create a new upstream WebSocket", "subpool_websocket_model_changed"}
		}
		return false, false, nil
	}
	s.mu.Unlock()
	account, continuation, requestErr := s.initialAccount(request, route)
	if requestErr != nil {
		return false, false, requestErr
	}
	if account.Provider == "" || account.Provider == domain.ProviderCodex {
		if !s.hub.forceHTTPBridge {
			bridge, pinErr := s.pinNativeAccount(route, account, continuation, request.Model)
			return bridge, bridge && !continuation, pinErr
		}
	}
	if !s.pinAccount(account, nil, false, "") {
		return false, false, &gatewayError{http.StatusServiceUnavailable, "WebSocket session is closing", "server_error"}
	}
	return true, !continuation, nil
}

func (s *responsesWSSession) initialAccount(request responsesWSRequest, route domain.KeyRoute) (domain.ProviderAccount, bool, *gatewayError) {
	if request.PreviousResponseID != "" {
		account, err := s.hub.server.store.ResolveSessionAccount(s.ctx, route.Key.ID, sessionHash(request.PreviousResponseID))
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return domain.ProviderAccount{}, true, &gatewayError{http.StatusBadRequest, "previous response was not found", "previous_response_not_found"}
			}
			return domain.ProviderAccount{}, true, &gatewayError{http.StatusServiceUnavailable, "session state is unavailable", "server_error"}
		}
		if !accountHealthy(account, s.hub.server.now()) {
			return domain.ProviderAccount{}, true, &gatewayError{http.StatusServiceUnavailable, "session account is unavailable", "subpool_session_account_unavailable"}
		}
		return account, true, nil
	}
	account := route.Account
	if route.Pool.Provider == domain.ProviderMixed && account.CredentialType == domain.CredentialAPIKey {
		if reassigned, err := s.hub.server.store.ReassignAPIKey(s.ctx, route.Key.ID, route.Pool.ID, nil); err == nil {
			account = reassigned
		}
	}
	if route.MembershipEnabled && accountHealthy(account, s.hub.server.now()) {
		return account, false, nil
	}
	reassigned, err := s.hub.server.store.ReassignAPIKey(s.ctx, route.Key.ID, route.Pool.ID, []string{account.ID})
	if err != nil {
		return domain.ProviderAccount{}, false, &gatewayError{http.StatusServiceUnavailable, "no eligible account", "subpool_no_eligible_account"}
	}
	return reassigned, false, nil
}

func (s *responsesWSSession) pinNativeAccount(route domain.KeyRoute, first domain.ProviderAccount, continuation bool, model string) (bool, *gatewayError) {
	account := first
	attempted := make([]string, 0, maxProviderAttempts)
	for attempt := 0; attempt < maxProviderAttempts; attempt++ {
		attempted = append(attempted, account.ID)
		if account.Provider == domain.ProviderOpenAICompatible {
			if !s.pinAccount(account, nil, false, "") {
				return false, &gatewayError{http.StatusServiceUnavailable, "WebSocket session is closing", "server_error"}
			}
			return true, nil
		}
		conn, current, retry, requestErr := s.dialCodex(account, model)
		if requestErr == nil {
			if !s.pinAccount(current, conn, true, model) {
				_ = conn.CloseNow()
				return false, &gatewayError{http.StatusServiceUnavailable, "WebSocket session is closing", "server_error"}
			}
			if !s.startWorker(s.readUpstream) {
				_ = conn.CloseNow()
				return false, &gatewayError{http.StatusServiceUnavailable, "WebSocket session is closing", "server_error"}
			}
			return false, nil
		}
		if continuation || !retry || attempt == maxProviderAttempts-1 {
			return false, requestErr
		}
		next, err := s.hub.server.store.ReassignAPIKey(s.ctx, route.Key.ID, route.Pool.ID, attempted)
		if err != nil {
			return false, requestErr
		}
		account = next
	}
	return false, &gatewayError{http.StatusServiceUnavailable, "no eligible account", "subpool_no_eligible_account"}
}

func (s *responsesWSSession) dialCodex(account domain.ProviderAccount, model string) (*websocket.Conn, domain.ProviderAccount, bool, *gatewayError) {
	conn, status, headers, err := s.dialCodexOnce(account, model)
	if err == nil {
		return conn, account, false, nil
	}
	if (status == http.StatusUnauthorized || status == http.StatusForbidden) && refreshableCredentials(account) {
		refreshed, refreshErr := s.hub.server.refresh(s.ctx, account)
		if refreshErr != nil {
			s.hub.upstreamFailures.Add(1)
			if s.hub.server.recordRefreshFailure(s.ctx, account.ID, refreshErr) {
				return nil, account, true, &gatewayError{http.StatusUnauthorized, "provider authentication failed", "provider_authentication_error"}
			}
			return nil, account, true, &gatewayError{http.StatusServiceUnavailable, "provider credential refresh is unavailable", "provider_error"}
		}
		conn, status, headers, err = s.dialCodexOnce(refreshed, model)
		if err == nil {
			return conn, refreshed, false, nil
		}
		account = refreshed
	}
	s.hub.upstreamFailures.Add(1)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		_ = s.hub.server.store.UpdateProviderStatus(s.ctx, account.ID, domain.AccountAuthFailed, nil)
		return nil, account, true, &gatewayError{http.StatusUnauthorized, "provider authentication failed", "provider_authentication_error"}
	case status == http.StatusTooManyRequests:
		retryAt := retryAfter(headers, s.hub.server.now())
		_ = s.hub.server.store.UpdateProviderStatus(s.ctx, account.ID, domain.AccountCoolingDown, &retryAt)
		return nil, account, true, &gatewayError{http.StatusTooManyRequests, "provider is rate limited", "subpool_rate_limited"}
	case status >= 500 || status == 0:
		s.hub.server.recordHealthFailure(s.ctx, account.ID, "websocket_connection_failed")
		return nil, account, true, &gatewayError{http.StatusServiceUnavailable, "provider WebSocket is unavailable", "provider_error"}
	default:
		return nil, account, false, &gatewayError{http.StatusBadGateway, "provider rejected WebSocket upgrade", "provider_error"}
	}
}

func (s *responsesWSSession) dialCodexOnce(account domain.ProviderAccount, model string) (*websocket.Conn, int, http.Header, error) {
	credentials, err := s.hub.server.credentials(account)
	if err != nil {
		return nil, 0, nil, err
	}
	installationID, err := codex.InstallationID(account.ID)
	if err != nil {
		return nil, 0, nil, err
	}
	wsURL, err := codex.ResponsesWebSocketURL(s.hub.codexUpstream)
	if err != nil {
		return nil, 0, nil, err
	}
	headers := codex.ResponsesWebSocketHeaders(s.headers, credentials, installationID, model, account.FastModeEnabled)
	dialCtx, cancel := context.WithTimeout(s.ctx, responsesWSDialTimeout)
	defer cancel()
	s.hub.upstreamDials.Add(1)
	conn, response, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		status := 0
		responseHeaders := make(http.Header)
		if response != nil {
			status = response.StatusCode
			responseHeaders = response.Header.Clone()
			if response.Body != nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
				_ = response.Body.Close()
			}
		}
		return nil, status, responseHeaders, err
	}
	conn.SetReadLimit(maxRequestBody)
	return conn, 0, nil, nil
}

func (s *responsesWSSession) pinAccount(account domain.ProviderAccount, upstream *websocket.Conn, native bool, upstreamModel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.account = account
	s.upstream = upstream
	s.upstreamModel = upstreamModel
	s.native = native
	s.pinned = true
	return true
}

func (s *responsesWSSession) validateContinuation(previous string) *gatewayError {
	if previous == "" {
		return nil
	}
	s.mu.Lock()
	_, local := s.responses[previous]
	accountID := s.account.ID
	s.mu.Unlock()
	if local {
		return nil
	}
	account, err := s.hub.server.store.ResolveSessionAccount(s.ctx, s.keyID, sessionHash(previous))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return &gatewayError{http.StatusBadRequest, "previous response was not found", "previous_response_not_found"}
		}
		return &gatewayError{http.StatusServiceUnavailable, "session state is unavailable", "server_error"}
	}
	if account.ID != accountID {
		return &gatewayError{http.StatusConflict, "previous response belongs to another account", "subpool_session_account_mismatch"}
	}
	return nil
}

func (s *responsesWSSession) preparePayload(request responsesWSRequest) ([]byte, *gatewayError) {
	s.mu.Lock()
	native := s.native
	account := s.account
	s.mu.Unlock()
	if native {
		installationID, err := codex.InstallationID(account.ID)
		if err != nil {
			return nil, &gatewayError{http.StatusInternalServerError, "device identity is unavailable", "server_error"}
		}
		payload, err := normalizeCodexWebSocketRequest(request.raw, installationID, account.FastModeEnabled)
		if err != nil {
			return nil, &gatewayError{http.StatusBadRequest, err.Error(), "invalid_request_error"}
		}
		return payload, nil
	}
	var body map[string]any
	if err := json.Unmarshal(request.raw, &body); err != nil {
		return nil, &gatewayError{http.StatusBadRequest, "invalid response.create message", "invalid_request_error"}
	}
	delete(body, "type")
	delete(body, "stream_id")
	body["stream"] = true
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, &gatewayError{http.StatusBadRequest, "invalid response.create message", "invalid_request_error"}
	}
	return payload, nil
}

func (s *responsesWSSession) reserveTurn(request responsesWSRequest, payload []byte) (*responsesWSTurn, *gatewayError) {
	streamID := request.streamID()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, &gatewayError{http.StatusServiceUnavailable, "WebSocket session is closing", "server_error"}
	}
	if s.pending >= responsesWSMaxTurnsPerConnection {
		s.mu.Unlock()
		return nil, &gatewayError{http.StatusTooManyRequests, "connection turn capacity exceeded", "subpool_capacity_exceeded"}
	}
	if request.StreamID != nil {
		if _, known := s.named[streamID]; !known && len(s.named) >= responsesWSMaxNamedStreams {
			s.mu.Unlock()
			return nil, &gatewayError{http.StatusBadRequest, "too many named streams", "invalid_stream_id"}
		}
	}
	accountID := s.account.ID
	s.mu.Unlock()
	if !s.hub.reserveAccountTurn(accountID) {
		return nil, &gatewayError{http.StatusServiceUnavailable, "account turn capacity exceeded", "subpool_capacity_exceeded"}
	}
	turn := &responsesWSTurn{streamID: streamID, accountID: accountID, model: request.Model, payload: payload}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.hub.releaseAccountTurn(accountID)
		return nil, &gatewayError{http.StatusServiceUnavailable, "WebSocket session is closing", "server_error"}
	}
	if request.StreamID != nil {
		s.named[streamID] = struct{}{}
	}
	stream := s.streams[streamID]
	if stream == nil {
		stream = &responsesWSStream{}
		s.streams[streamID] = stream
	}
	stream.turns = append(stream.turns, turn)
	s.pending++
	s.mu.Unlock()
	return turn, nil
}

func (s *responsesWSSession) writeUpstream(payload []byte) error {
	s.mu.Lock()
	upstream := s.upstream
	s.mu.Unlock()
	if upstream == nil {
		return errors.New("upstream WebSocket is unavailable")
	}
	ctx, cancel := context.WithTimeout(s.ctx, responsesWSWriteTimeout)
	defer cancel()
	return upstream.Write(ctx, websocket.MessageText, payload)
}

func (s *responsesWSSession) readUpstream() {
	for {
		s.mu.Lock()
		upstream := s.upstream
		s.mu.Unlock()
		messageType, payload, err := upstream.Read(s.ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			if status < websocket.StatusNormalClosure {
				status = websocket.StatusInternalError
			}
			s.close(status, "upstream connection closed")
			return
		}
		if messageType != websocket.MessageText {
			s.close(websocket.StatusUnsupportedData, "upstream sent binary data")
			return
		}
		event := parseResponsesWSEvent(payload)
		turn := s.currentTurn(event.StreamID)
		if turn != nil {
			s.observeTurn(turn, payload, event.Type)
		}
		if err = s.writeClient(payload); err != nil {
			s.close(websocket.StatusInternalError, "client write failed")
			return
		}
		if turn != nil && terminalResponsesWSEvent(event.Type) {
			s.finishTurn(turn)
		}
	}
}

func (s *responsesWSSession) startInitialBridge(turn *responsesWSTurn, route domain.KeyRoute, canFailover bool) {
	response, retry, resettable := s.openInitialBridge(turn, route, canFailover)
	if response == nil {
		code, message := responseWSRetryError(retry)
		s.sendError(turn.streamID, code, message)
		s.finishTurn(turn)
		if resettable {
			s.resetBridgePin()
		}
		return
	}
	s.startBridge(turn.streamID, response)
}

func (s *responsesWSSession) openInitialBridge(turn *responsesWSTurn, route domain.KeyRoute, canFailover bool) (*http.Response, retryReason, bool) {
	attempted := make([]string, 0, maxProviderAttempts)
	for attempt := 0; attempt < maxProviderAttempts; attempt++ {
		s.mu.Lock()
		account := s.account
		s.mu.Unlock()
		if len(attempted) == 0 || attempted[len(attempted)-1] != account.ID {
			attempted = append(attempted, account.ID)
		}
		response, retry, complete := s.bridgeResponse(turn, route, account)
		if complete {
			return response, "", false
		}
		if !canFailover {
			return nil, retry, false
		}
		if !bridgeRetrySafe(retry) {
			return nil, retry, retry != retryTransport
		}
		moved := false
		for len(attempted) < maxProviderAttempts {
			next, err := s.hub.server.store.ReassignAPIKey(s.ctx, s.keyID, s.poolID, attempted)
			if err != nil {
				return nil, retry, true
			}
			attempted = append(attempted, next.ID)
			if s.transferBridgeAccount(turn, next) {
				moved = true
				break
			}
		}
		if !moved {
			return nil, retry, true
		}
	}
	return nil, retryUnavailable, true
}

func (s *responsesWSSession) transferBridgeAccount(turn *responsesWSTurn, account domain.ProviderAccount) bool {
	if account.ID == "" || !s.hub.reserveAccountTurn(account.ID) {
		return false
	}
	s.mu.Lock()
	if s.closed || s.native || !s.pinned || turn.finished {
		s.mu.Unlock()
		s.hub.releaseAccountTurn(account.ID)
		return false
	}
	previousID := turn.accountID
	s.account = account
	turn.accountID = account.ID
	s.mu.Unlock()
	s.hub.releaseAccountTurn(previousID)
	return true
}

func (s *responsesWSSession) resetBridgePin() {
	s.mu.Lock()
	if !s.closed && !s.native && s.pending == 0 {
		s.pinned = false
		s.account = domain.ProviderAccount{}
	}
	s.mu.Unlock()
}

func (s *responsesWSSession) startBridge(streamID string, initial *http.Response) {
	s.mu.Lock()
	stream := s.streams[streamID]
	if s.closed || stream == nil || stream.bridgeRunning {
		s.mu.Unlock()
		if initial != nil {
			_ = initial.Body.Close()
		}
		return
	}
	stream.bridgeRunning = true
	s.mu.Unlock()
	if !s.startWorker(func() { s.runBridge(streamID, initial) }) && initial != nil {
		_ = initial.Body.Close()
	}
}

func (s *responsesWSSession) runBridge(streamID string, initial *http.Response) {
	for {
		s.mu.Lock()
		stream := s.streams[streamID]
		if stream == nil || len(stream.turns) == 0 {
			if stream != nil {
				stream.bridgeRunning = false
			}
			s.mu.Unlock()
			return
		}
		turn := stream.turns[0]
		s.mu.Unlock()
		if initial != nil {
			s.forwardBridgeTurnResponse(turn, initial)
			initial = nil
		} else {
			s.runBridgeTurn(turn)
		}
		s.finishTurn(turn)
		if s.ctx.Err() != nil {
			return
		}
	}
}

func (s *responsesWSSession) runBridgeTurn(turn *responsesWSTurn) {
	route, requestErr := s.resolveRoute()
	if requestErr != nil {
		s.sendError(turn.streamID, requestErr.code, requestErr.message)
		return
	}
	account := route.Account
	response, retry, complete := s.bridgeResponse(turn, route, account)
	if !complete || response == nil {
		code, message := responseWSRetryError(retry)
		s.sendError(turn.streamID, code, message)
		return
	}
	s.forwardBridgeTurnResponse(turn, response)
}

func (s *responsesWSSession) bridgeResponse(turn *responsesWSTurn, route domain.KeyRoute, account domain.ProviderAccount) (*http.Response, retryReason, bool) {
	request, err := http.NewRequestWithContext(s.ctx, http.MethodPost, "http://subpool.local/v1/responses", nil)
	if err != nil {
		return nil, retryUnavailable, false
	}
	request.Header = s.headers.Clone()
	request.Header.Set("Accept", "text/event-stream")
	upstreamRequest := upstreamRequest{kind: "responses", model: turn.model, body: turn.payload, codexBody: turn.payload}
	return s.hub.server.attemptAccount(request, route, upstreamRequest, account)
}

func (s *responsesWSSession) forwardBridgeTurnResponse(turn *responsesWSTurn, response *http.Response) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code, message := readResponsesWSHTTPError(response)
		s.sendError(turn.streamID, code, message)
		return
	}
	if err := s.forwardBridgeResponse(turn, response); err != nil && s.ctx.Err() == nil {
		s.sendError(turn.streamID, "provider_error", "provider response stream failed")
	}
}

func (s *responsesWSSession) finishTurn(turn *responsesWSTurn) {
	s.mu.Lock()
	if turn == nil || turn.finished {
		s.mu.Unlock()
		return
	}
	turn.finished = true
	stream := s.streams[turn.streamID]
	if stream != nil {
		for index, candidate := range stream.turns {
			if candidate == turn {
				stream.turns = append(stream.turns[:index], stream.turns[index+1:]...)
				break
			}
		}
	}
	if s.pending > 0 {
		s.pending--
	}
	successfulTerminal := turn.terminal == "response.completed" || turn.terminal == "response.incomplete"
	if successfulTerminal && turn.response != "" {
		s.rememberResponseLocked(turn.response)
	}
	accountID := turn.accountID
	s.mu.Unlock()
	s.hub.releaseAccountTurn(accountID)
	s.touch()
	if !successfulTerminal {
		return
	}
	if turn.input > 0 || turn.output > 0 {
		s.hub.server.addUsage(s.keyID, s.hub.server.usageEventHash(turn.response, s.hub.server.randomUsageEventHash()), turn.model, turn.input, turn.output)
	}
	if turn.response != "" {
		s.hub.server.saveSession(s.keyID, s.poolID, turn.response, accountID)
	}
	if s.hub.server.activity.ShouldRecord(accountID, s.keyID, s.hub.server.now()) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = s.hub.server.store.RecordRequestSuccess(ctx, accountID, s.keyID, s.hub.server.now())
		cancel()
	}
}

func (s *responsesWSSession) writeClient(payload []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, responsesWSWriteTimeout)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, payload)
}

func (s *responsesWSSession) sendError(streamID, code, message string) {
	value := map[string]any{
		"type":  "error",
		"error": map[string]any{"type": code, "code": code, "message": message},
	}
	if streamID != "" {
		value["stream_id"] = streamID
	}
	payload, _ := json.Marshal(value)
	_ = s.writeClient(payload)
}

func (s *responsesWSSession) startWorker(run func()) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		run()
	}()
	return true
}

func (s *responsesWSSession) touch() { s.lastActive.Store(time.Now().UnixNano()) }

func (s *responsesWSSession) monitorIdle() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			s.mu.Lock()
			pending := s.pending
			s.mu.Unlock()
			last := time.Unix(0, s.lastActive.Load())
			if pending == 0 && now.Sub(last) >= responsesWSIdleTimeout {
				s.close(websocket.StatusGoingAway, "idle timeout")
				return
			}
		}
	}
}

func (s *responsesWSSession) close(status websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		upstream := s.upstream
		turns := make([]*responsesWSTurn, 0, s.pending)
		for _, stream := range s.streams {
			turns = append(turns, stream.turns...)
		}
		s.mu.Unlock()
		s.cancel()
		if upstream != nil {
			_ = upstream.CloseNow()
		}
		_ = s.conn.Close(status, reason)
		for _, turn := range turns {
			s.finishTurn(turn)
		}
	})
}
