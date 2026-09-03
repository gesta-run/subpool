package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"github.com/gesta-run/subpool/internal/store"
)

type responsesWSBridgeProvider struct {
	payload chan []byte
	headers chan http.Header
}

func (p *responsesWSBridgeProvider) Responses(_ context.Context, body []byte, headers http.Header, _ openaicompat.Credentials) (*http.Response, error) {
	p.payload <- append([]byte(nil), body...)
	if p.headers != nil {
		p.headers <- headers.Clone()
	}
	return sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-bridge\",\"model\":\"gpt-test\",\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n"), nil
}

func (p *responsesWSBridgeProvider) ChatCompletions(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error) {
	return nil, nil
}

func TestResponsesWebSocketHTTPBridge(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	account := compatibleAccountWithCipher(t, cipher, "compatible-account")
	st.route.Account = account
	st.route.Pool.Provider = domain.ProviderOpenAICompatible
	st.sessionAccount = account
	provider := &responsesWSBridgeProvider{payload: make(chan []byte, 1), headers: make(chan http.Header, 1)}
	server.compatible = provider
	server.WithResponsesWebSocket(true, false, "")

	client, cleanup := dialResponsesWSTestServer(t, server, plain)
	defer cleanup()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err := client.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","stream_id":"worker-1","model":"gpt-test","input":"hello"}`))
	cancelWrite()
	if err != nil {
		t.Fatal(err)
	}
	readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
	_, payload, err := client.Read(readCtx)
	cancelRead()
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err = json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	if event["type"] != "response.completed" || event["stream_id"] != "worker-1" {
		t.Fatalf("unexpected event: %s", payload)
	}
	select {
	case upstream := <-provider.payload:
		if st.allowCalls != 1 {
			t.Fatalf("rate limit calls = %d", st.allowCalls)
		}
		var body map[string]any
		if err = json.Unmarshal(upstream, &body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["type"] != nil || body["stream_id"] != nil {
			t.Fatalf("unexpected bridge body: %s", upstream)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge request was not sent")
	}
	if headers := <-provider.headers; headers.Get("Accept") != "text/event-stream" {
		t.Fatalf("Accept = %q", headers.Get("Accept"))
	}
}

type responsesWSRetryProvider struct{ calls atomic.Int32 }

func (p *responsesWSRetryProvider) Responses(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error) {
	if p.calls.Add(1) == 1 {
		return sseResponse(http.StatusTooManyRequests, "rate limited"), nil
	}
	return sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-retried\"}}\n\n"), nil
}

func (p *responsesWSRetryProvider) ChatCompletions(context.Context, []byte, http.Header, openaicompat.Credentials) (*http.Response, error) {
	return nil, nil
}

func TestResponsesWebSocketFirstBridgeTurnFailsOver(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	first := compatibleAccountWithCipher(t, cipher, "compatible-account-1")
	second := compatibleAccountWithCipher(t, cipher, "compatible-account-2")
	st.route.Account = first
	st.route.Pool.Provider = domain.ProviderOpenAICompatible
	st.reassigned = second
	provider := &responsesWSRetryProvider{}
	server.compatible = provider
	server.WithResponsesWebSocket(true, false, "")

	client, cleanup := dialResponsesWSTestServer(t, server, plain)
	defer cleanup()
	writeResponsesWSMessage(t, client, `{"type":"response.create","model":"gpt-test","input":"hello"}`)
	event := readResponsesWSMessage(t, client)
	if event["type"] != "response.completed" || provider.calls.Load() != 2 {
		t.Fatalf("event=%#v calls=%d", event, provider.calls.Load())
	}
}

func TestResponsesWebSocketRejectsDisabledPinnedAccount(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	account := compatibleAccountWithCipher(t, cipher, "compatible-account")
	st.route.Account = account
	st.route.Pool.Provider = domain.ProviderOpenAICompatible
	provider := &responsesWSBridgeProvider{payload: make(chan []byte, 2)}
	server.compatible = provider
	server.WithResponsesWebSocket(true, false, "")

	client, cleanup := dialResponsesWSTestServer(t, server, plain)
	defer cleanup()
	writeResponsesWSMessage(t, client, `{"type":"response.create","model":"gpt-test","input":"first"}`)
	if event := readResponsesWSMessage(t, client); event["type"] != "response.completed" {
		t.Fatalf("first event = %#v", event)
	}
	st.route.MembershipEnabled = false
	writeResponsesWSMessage(t, client, `{"type":"response.create","model":"gpt-test","input":"second"}`)
	event := readResponsesWSMessage(t, client)
	errorValue, _ := event["error"].(map[string]any)
	if event["type"] != "error" || errorValue["code"] != "subpool_session_account_unavailable" {
		t.Fatalf("event = %#v", event)
	}
}

func TestResponsesWebSocketPublishesContinuationBeforeFinishing(t *testing.T) {
	server, st, _, _ := newTestServer(t)
	st.sessionErr = store.ErrNotFound
	session := &responsesWSSession{
		hub: server.responsesWS, ctx: context.Background(), keyID: st.route.Key.ID,
		account: st.route.Account, responses: make(map[string]struct{}),
	}
	turn := &responsesWSTurn{accountID: st.route.Account.ID}
	session.observeTurn(turn, []byte(`{"type":"response.completed","response":{"id":"resp-local"}}`), "response.completed")
	if requestErr := session.validateContinuation("resp-local"); requestErr != nil {
		t.Fatalf("continuation error = %#v", requestErr)
	}
}

func TestResponsesWebSocketNativeCodex(t *testing.T) {
	upstreamPayload := make(chan []byte, 1)
	upstreamHeaders := make(chan http.Header, 1)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		upstreamHeaders <- r.Header.Clone()
		messageType, payload, err := conn.Read(r.Context())
		if err != nil || messageType != websocket.MessageText {
			return
		}
		upstreamPayload <- payload
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.created","response":{"id":"resp-native","model":"gpt-test"}}`))
		_ = conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"response.completed","response":{"id":"resp-native","model":"gpt-test","usage":{"input_tokens":4,"output_tokens":3}}}`))
		<-release
	}))
	defer upstream.Close()

	server, st, _, plain := newTestServer(t)
	server.WithResponsesWebSocket(true, false, upstream.URL)
	client, cleanup := dialResponsesWSTestServer(t, server, plain)
	defer cleanup()
	defer close(release)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err := client.Write(writeCtx, websocket.MessageText, []byte(`{"type":"response.create","model":"gpt-test","stream":true,"input":"hello"}`))
	cancelWrite()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 2; index++ {
		readCtx, cancelRead := context.WithTimeout(context.Background(), time.Second)
		_, _, err = client.Read(readCtx)
		cancelRead()
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case payload := <-upstreamPayload:
		var body map[string]any
		if err = json.Unmarshal(payload, &body); err != nil {
			t.Fatal(err)
		}
		metadata, _ := body["client_metadata"].(map[string]any)
		installationID, _ := codex.InstallationID(st.route.Account.ID)
		if body["store"] != false || body["stream"] != nil || metadata["x-codex-installation-id"] != installationID {
			t.Fatalf("unexpected native body: %s", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("native request was not sent")
	}
	select {
	case headers := <-upstreamHeaders:
		if headers.Get("OpenAI-Beta") == "" || headers.Get("X-Codex-Installation-Id") == "" {
			t.Fatalf("missing Codex WebSocket headers: %#v", headers)
		}
	case <-time.After(time.Second):
		t.Fatal("native WebSocket was not dialed")
	}
}

func TestParseResponsesWSRequestRejectsDuplicateControlField(t *testing.T) {
	_, requestErr := parseResponsesWSRequest([]byte(`{"type":"response.create","previous_response_id":"first","previous_response_id":"second"}`))
	if requestErr == nil || !strings.Contains(requestErr.message, "duplicate previous_response_id") {
		t.Fatalf("request error = %#v", requestErr)
	}
}

func TestParseResponsesWSRequestValidatesStreamID(t *testing.T) {
	for _, streamID := range []string{"", "has space", "worker/1", "任务"} {
		payload, _ := json.Marshal(map[string]any{"type": "response.create", "stream_id": streamID})
		if _, requestErr := parseResponsesWSRequest(payload); requestErr == nil || requestErr.code != "invalid_stream_id" {
			t.Fatalf("stream_id %q was accepted", streamID)
		}
	}
	valid := strings.Repeat("a", responsesWSMaxStreamIDBytes)
	payload, _ := json.Marshal(map[string]any{"type": "response.create", "stream_id": valid})
	if _, requestErr := parseResponsesWSRequest(payload); requestErr != nil {
		t.Fatalf("valid stream_id was rejected: %#v", requestErr)
	}
}

func writeResponsesWSMessage(t *testing.T, client *websocket.Conn, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		t.Fatal(err)
	}
}

func readResponsesWSMessage(t *testing.T, client *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, payload, err := client.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err = json.Unmarshal(payload, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func dialResponsesWSTestServer(t *testing.T, gateway *Server, key string) (*websocket.Conn, func()) {
	t.Helper()
	mux := http.NewServeMux()
	gateway.Register(mux)
	server := httptest.NewServer(mux)
	headers := http.Header{"Authorization": {"Bearer " + key}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	conn, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", &websocket.DialOptions{HTTPHeader: headers})
	cancel()
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return conn, func() {
		conn.CloseNow()
		gateway.CloseResponsesWebSockets()
		server.Close()
	}
}
