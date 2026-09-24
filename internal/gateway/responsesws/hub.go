package responsesws

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

type Hub struct {
	backend         Backend
	enabled         bool
	forceHTTPBridge bool
	codexUpstream   string

	mu                sync.Mutex
	sessions          map[*responsesWSSession]struct{}
	connections       map[string]int
	accountTurns      map[string]int
	activeConnections int
	closing           bool

	connectionsTotal  atomic.Int64
	connectionsReject atomic.Int64
	turnsTotal        atomic.Int64
	turnsRejected     atomic.Int64
	upstreamDials     atomic.Int64
	upstreamFailures  atomic.Int64
}

func New(backend Backend) *Hub {
	return &Hub{
		backend:      backend,
		sessions:     make(map[*responsesWSSession]struct{}),
		connections:  make(map[string]int),
		accountTurns: make(map[string]int),
	}
}

func (h *Hub) Configure(enabled, forceHTTPBridge bool, codexUpstream string) {
	h.enabled = enabled
	h.forceHTTPBridge = forceHTTPBridge
	h.codexUpstream = strings.TrimRight(strings.TrimSpace(codexUpstream), "/")
}

func (h *Hub) Enabled() bool { return h.enabled }

func (h *Hub) Handle(w http.ResponseWriter, r *http.Request) {
	route, keyDigest, requestErr := h.backend.Authenticate(r.Context(), r.Header.Get("Authorization"), "responses")
	if requestErr != nil {
		writeOpenAIError(w, requestErr.Status, requestErr.Message, requestErr.Code)
		return
	}
	if !h.reserveConnection(route.Key.ID) {
		writeOpenAIError(w, http.StatusServiceUnavailable, "WebSocket connection capacity exceeded", "subpool_capacity_exceeded")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		h.releaseConnection(route.Key.ID)
		return
	}
	bodyState := h.backend.RequestBodyState()
	conn.SetReadLimit(bodyState.MaxBytes)
	ctx, cancel := context.WithTimeout(r.Context(), responsesWSLifetime)
	session := &responsesWSSession{
		hub: h, conn: conn, ctx: ctx, cancel: cancel, keyDigest: keyDigest,
		keyID: route.Key.ID, poolID: route.Pool.ID, headers: r.Header.Clone(), done: make(chan struct{}),
		streams: make(map[string]*responsesWSStream),
		named:   make(map[string]struct{}), responses: make(map[string]struct{}),
	}
	session.touch()
	if !h.addSession(session) {
		cancel()
		_ = conn.CloseNow()
		h.releaseConnection(route.Key.ID)
		return
	}
	h.connectionsTotal.Add(1)
	defer func() {
		session.close(websocket.StatusNormalClosure, "")
		session.workers.Wait()
		h.removeSession(session)
		close(session.done)
	}()
	session.startWorker(session.monitorIdle)
	session.readClient()
}

func (h *Hub) reserveConnection(keyID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing || h.activeConnections >= responsesWSMaxConnections || h.connections[keyID] >= responsesWSMaxConnectionsPerKey {
		h.connectionsReject.Add(1)
		return false
	}
	h.activeConnections++
	h.connections[keyID]++
	return true
}

func (h *Hub) addSession(session *responsesWSSession) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.sessions[session] = struct{}{}
	return true
}

func (h *Hub) releaseConnection(keyID string) {
	h.mu.Lock()
	if h.activeConnections > 0 {
		h.activeConnections--
	}
	if h.connections[keyID] <= 1 {
		delete(h.connections, keyID)
	} else {
		h.connections[keyID]--
	}
	h.mu.Unlock()
}

func (h *Hub) removeSession(session *responsesWSSession) {
	h.mu.Lock()
	delete(h.sessions, session)
	h.mu.Unlock()
	h.releaseConnection(session.keyID)
}

func (h *Hub) reserveAccountTurn(accountID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.accountTurns[accountID] >= responsesWSMaxTurnsPerAccount {
		return false
	}
	h.accountTurns[accountID]++
	return true
}

func (h *Hub) releaseAccountTurn(accountID string) {
	h.mu.Lock()
	if h.accountTurns[accountID] <= 1 {
		delete(h.accountTurns, accountID)
	} else {
		h.accountTurns[accountID]--
	}
	h.mu.Unlock()
}

func (h *Hub) CloseAll() {
	h.mu.Lock()
	h.closing = true
	sessions := make([]*responsesWSSession, 0, len(h.sessions))
	for session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()
	for _, session := range sessions {
		go session.close(websocket.StatusServiceRestart, "service restart")
	}
	for _, session := range sessions {
		<-session.done
	}
}

func (h *Hub) CloseAccount(accountID string) {
	h.mu.Lock()
	sessions := make([]*responsesWSSession, 0, len(h.sessions))
	for session := range h.sessions {
		sessions = append(sessions, session)
	}
	h.mu.Unlock()
	for _, session := range sessions {
		session.mu.Lock()
		matches := session.pinned && session.account.ID == accountID
		session.mu.Unlock()
		if matches {
			go session.close(websocket.StatusServiceRestart, "account routing settings changed")
		}
	}
}

func (h *Hub) Metrics() string {
	h.mu.Lock()
	activeConnections := len(h.sessions)
	activeTurns := 0
	for _, count := range h.accountTurns {
		activeTurns += count
	}
	h.mu.Unlock()
	return fmt.Sprintf(
		"# TYPE subpool_responses_ws_connections_active gauge\nsubpool_responses_ws_connections_active %d\n"+
			"# TYPE subpool_responses_ws_connections_total counter\nsubpool_responses_ws_connections_total %d\n"+
			"# TYPE subpool_responses_ws_connections_rejected_total counter\nsubpool_responses_ws_connections_rejected_total %d\n"+
			"# TYPE subpool_responses_ws_turns_active gauge\nsubpool_responses_ws_turns_active %d\n"+
			"# TYPE subpool_responses_ws_turns_total counter\nsubpool_responses_ws_turns_total %d\n"+
			"# TYPE subpool_responses_ws_turns_rejected_total counter\nsubpool_responses_ws_turns_rejected_total %d\n"+
			"# TYPE subpool_responses_ws_upstream_dials_total counter\nsubpool_responses_ws_upstream_dials_total %d\n"+
			"# TYPE subpool_responses_ws_upstream_failures_total counter\nsubpool_responses_ws_upstream_failures_total %d\n",
		activeConnections, h.connectionsTotal.Load(), h.connectionsReject.Load(), activeTurns,
		h.turnsTotal.Load(), h.turnsRejected.Load(), h.upstreamDials.Load(), h.upstreamFailures.Load(),
	)
}

func writeOpenAIError(w http.ResponseWriter, status int, message, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": message, "type": code, "code": code}})
}
