package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gesta-run/subpool/internal/domain"
)

func TestRequestBodySettingsApplyAcrossInstances(t *testing.T) {
	first, st, _, _ := newTestServer(t)
	second, _, _, _ := newTestServer(t)
	second.store = st
	servers := []*Server{first, second}
	for _, server := range servers {
		server.WithRequestBodyLimits(16, 64, time.Minute)
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"illustrative-input"}`))
		if _, _, _, ok := server.readRequest(httptest.NewRecorder(), request); ok {
			t.Fatal("the deployment default body limit was ignored")
		}
	}
	st.setSettings(domain.Settings{MaxRequestBodyBytes: 128, MaxInflightRequestBodyBytes: 512})
	for _, server := range servers {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test","input":"illustrative-input"}`))
		_, _, release, ok := server.readRequest(httptest.NewRecorder(), request)
		if !ok {
			t.Fatal("a saved limit was not applied to the next request on another instance")
		}
		release()
	}
}

func TestRequestBodySettingsPreserveInflightReservations(t *testing.T) {
	server, st, _, _ := newTestServer(t)
	st.setSettings(domain.Settings{MaxRequestBodyBytes: 1 << 20, MaxInflightRequestBodyBytes: 4 << 20})
	started, resume := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	body := `{"model":"gpt-test","input":"` + strings.Repeat("a", 128<<10) + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	request.Body = io.NopCloser(&blockingBodyReader{reader: strings.NewReader(body), started: started, release: resume})
	finished := make(chan bool, 1)
	go func() {
		_, _, release, ok := server.readRequest(httptest.NewRecorder(), request)
		if ok {
			release()
		}
		finished <- ok
	}()
	<-started
	before := server.requestBodyBudget.used.Load()
	st.setSettings(domain.Settings{MaxRequestBodyBytes: 64, MaxInflightRequestBodyBytes: 256})
	if used := server.requestBodyBudget.used.Load(); used != before || used == 0 {
		t.Fatal("changing settings reset in-flight accounting")
	}
	newRequest := func() (*httptest.ResponseRecorder, func(), bool) {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
		recorder := httptest.NewRecorder()
		_, _, release, ok := server.readRequest(recorder, request)
		return recorder, release, ok
	}
	recorder, _, ok := newRequest()
	if ok || recorder.Code != http.StatusServiceUnavailable {
		t.Fatal("a lower budget ignored old reservations")
	}
	close(resume)
	if !<-finished {
		t.Fatal("changing settings interrupted an accepted request")
	}
	_, release, ok := newRequest()
	if !ok {
		t.Fatal("capacity did not recover after the old request finished")
	}
	release()
	if used := server.requestBodyBudget.used.Load(); used != 0 {
		t.Fatalf("request body reservation leaked: %d", used)
	}
}

func TestRequestBodySettingsFailClosed(t *testing.T) {
	for _, invalid := range []domain.Settings{{MaxRequestBodyBytes: 128, MaxInflightRequestBodyBytes: 256}, {MaxRequestBodyBytes: -1, MaxInflightRequestBodyBytes: 512}} {
		server, st, _, _ := newTestServer(t)
		st.setSettings(invalid)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`))
		_, _, _, ok := server.readRequest(recorder, request)
		if ok || recorder.Code != http.StatusServiceUnavailable || server.requestBodyBudget.used.Load() != 0 {
			t.Fatal("invalid persisted settings were used")
		}
	}
	server, st, _, _ := newTestServer(t)
	st.settingsErr = errors.New("illustrative database unavailable")
	recorder := httptest.NewRecorder()
	_, _, _, ok := server.readRequest(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-test"}`)))
	if ok || recorder.Code != http.StatusServiceUnavailable {
		t.Fatal("unavailable settings were silently ignored")
	}
}

func TestWebSocketRequestBodySettingsUpdateOnOpenConnection(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	server.WithRequestBodyLimits(128, 512, time.Second)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-illustrative\",\"output\":[],\"usage\":{}}}\n\n")}
	server.WithResponsesWebSocket(true, true, "")
	client, cleanup := dialResponsesWSTestServer(t, server, plain)
	defer cleanup()
	st.setSettings(domain.Settings{MaxRequestBodyBytes: 1024, MaxInflightRequestBodyBytes: 4096})
	writeResponsesWSMessage(t, client, `{"type":"response.create","model":"gpt-test","input":"`+strings.Repeat("a", 256)+`"}`)
	if event := readResponsesWSMessage(t, client); event["type"] != "response.completed" {
		t.Fatalf("an open connection retained its old size limit: %#v", event)
	}
	deadline := time.Now().Add(time.Second)
	for server.requestBodyBudget.used.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the completed WebSocket turn retained its body reservation")
		}
		time.Sleep(time.Millisecond)
	}
	st.setSettings(domain.Settings{MaxRequestBodyBytes: 64, MaxInflightRequestBodyBytes: 256})
	writeResponsesWSMessage(t, client, `{"type":"response.create","model":"gpt-test","input":"`+strings.Repeat("a", 64)+`"}`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := client.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("an open connection ignored its new lower size limit: %v", err)
	}
}
