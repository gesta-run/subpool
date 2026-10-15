package responsesws

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gesta-run/subpool/internal/domain"
)

func TestConnectionAdmissionLimitsAndRelease(t *testing.T) {
	h := New(nil)
	for i := 0; i < responsesWSMaxConnectionsPerKey; i++ {
		if !h.reserveConnection("employee") {
			t.Fatalf("connection %d rejected", i+1)
		}
	}
	if h.reserveConnection("employee") {
		t.Fatal("per-key limit was not enforced")
	}
	if !h.reserveConnection("other-employee") {
		t.Fatal("one employee exhausted another employee's capacity")
	}
	h.releaseConnection("employee")
	if !h.reserveConnection("employee") {
		t.Fatal("released capacity was not reusable")
	}
	for h.activeConnections < responsesWSMaxConnections {
		if !h.reserveConnection(fmt.Sprintf("key-%d", h.activeConnections)) {
			t.Fatal("connection rejected before global limit")
		}
	}
	if h.reserveConnection("new-key") {
		t.Fatal("global limit was not enforced")
	}
	h.closing = true
	if h.reserveConnection("new-key") {
		t.Fatal("connection accepted during shutdown")
	}
	metrics := h.Metrics()
	for _, want := range []string{
		"subpool_responses_ws_connections_rejected_total 3\n",
		`subpool_responses_ws_connection_rejections_total{reason="per_key_limit"} 1`,
		`subpool_responses_ws_connection_rejections_total{reason="global_limit"} 1`,
		`subpool_responses_ws_connection_rejections_total{reason="closing"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Fatalf("missing metric %q in %s", want, metrics)
		}
	}
}

func TestConcurrentConnectionAdmission(t *testing.T) {
	h := New(nil)
	var accepted atomic.Int64
	var workers sync.WaitGroup
	for i := 0; i < 2*responsesWSMaxConnectionsPerKey; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if h.reserveConnection("employee") {
				accepted.Add(1)
			}
		}()
	}
	workers.Wait()
	if accepted.Load() != int64(responsesWSMaxConnectionsPerKey) {
		t.Fatalf("accepted = %d", accepted.Load())
	}
	for i := int64(0); i < accepted.Load(); i++ {
		h.releaseConnection("employee")
	}
	if h.activeConnections != 0 || len(h.connections) != 0 {
		t.Fatal("connection reservations leaked")
	}
}

type connectionTestBackend struct{ Backend }

func (connectionTestBackend) Authenticate(context.Context, string, string) (domain.KeyRoute, []byte, *RequestError) {
	return domain.KeyRoute{Key: domain.APIKey{ID: "employee"}}, nil, nil
}

func TestEmployeeCanOpenMoreThanEightWebSockets(t *testing.T) {
	h := New(connectionTestBackend{})
	server := httptest.NewServer(http.HandlerFunc(h.Handle))
	defer server.Close()
	defer h.CloseAll()
	var clients []*websocket.Conn
	defer func() {
		for _, client := range clients {
			_ = client.CloseNow()
		}
	}()
	for i := 0; i < 9; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		client, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		cancel()
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if err != nil {
			t.Fatalf("connection %d: %v", i+1, err)
		}
		clients = append(clients, client)
	}
	if h.connectionsReject.Load() != 0 {
		t.Fatal("normal parallel sessions were rejected")
	}
}
