package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func assertRoutingCount(t *testing.T, server *Server, transport, binding, count string) {
	t.Helper()
	want := `subpool_codex_routing_requests_total{transport="` + transport + `",binding="` + binding + `"} ` + count + "\n"
	if metrics := server.ResponsesWebSocketMetrics(); !strings.Contains(metrics, want) {
		t.Fatalf("missing %q in metrics:\n%s", want, metrics)
	}
}

func TestRoutingObservationHTTPRetryCountedOnce(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.reassigned = accountWithCredentials(t, "other-account", "illustrative-token")
	provider.responses = []*http.Response{
		sseResponse(http.StatusTooManyRequests, "limited"),
		sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"usage\":{}}}\n\n"),
	}
	r := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if r.Code != http.StatusOK || len(provider.credentials) != 2 {
		t.Fatalf("status=%d attempts=%d", r.Code, len(provider.credentials))
	}
	assertRoutingCount(t, server, "http", "unbound", "1")
	assertRoutingCount(t, server, "http", "continuation", "0")
}

func TestRoutingObservationHTTPContinuation(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.sessionAccount = accountWithCredentials(t, "original-account", "illustrative-token")
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-next\",\"usage\":{}}}\n\n")}
	r := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","previous_response_id":"resp-original","input":"continue"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("status=%d", r.Code)
	}
	assertRoutingCount(t, server, "http", "unbound", "0")
	assertRoutingCount(t, server, "http", "continuation", "1")
}

func TestRoutingObservationWebSocketBindingAndBridge(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.sessionAccount = st.route.Account
	for range 3 {
		provider.responses = append(provider.responses, sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-test\",\"usage\":{}}}\n\n"))
	}
	server.WithResponsesWebSocket(true, true, "")
	client, cleanup := dialResponsesWSTestServer(t, server, plain)
	defer cleanup()
	for _, body := range []string{
		`{"type":"response.create","model":"gpt-test","input":"hello"}`,
		`{"type":"response.create","model":"gpt-test","previous_response_id":"resp-test","input":"continue"}`,
		`{"type":"response.create","model":"gpt-test","input":"full context"}`,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := client.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
			cancel()
			t.Fatal(err)
		}
		for {
			_, payload, err := client.Read(ctx)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			if strings.Contains(string(payload), `"type":"error"`) {
				cancel()
				t.Fatalf("unexpected error: %s", payload)
			}
			if strings.Contains(string(payload), "response.completed") {
				break
			}
		}
		cancel()
	}
	for _, binding := range []string{"unbound", "continuation", "connection"} {
		assertRoutingCount(t, server, "websocket", binding, "1")
		assertRoutingCount(t, server, "http", binding, "0")
	}
}
