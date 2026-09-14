package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type blockingBodyReader struct {
	reader  io.Reader
	started chan struct{}
	release chan struct{}
}

func (r *blockingBodyReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
		<-r.release
	}
	return r.reader.Read(buffer)
}

func TestReadRequestAcceptsExactBodyLimitAndReleasesReservation(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	body := `{"model":"gpt-test"}`
	server.WithRequestBodyLimits(int64(len(body)), int64(len(body))*maxHTTPRequestBodyCopies, time.Minute)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))

	got, meta, release, ok := server.readRequest(recorder, request)
	if !ok || string(got) != body || meta.Model != "gpt-test" {
		t.Fatalf("ok=%t body=%q meta=%#v response=%s", ok, got, meta, recorder.Body.String())
	}
	wantReserved := int64(len(body)) * responsesHTTPRequestBodyCopies
	if used := server.requestBodyBudget.used.Load(); used != wantReserved {
		t.Fatalf("reserved bytes = %d, want %d", used, wantReserved)
	}
	release()
	if used := server.requestBodyBudget.used.Load(); used != 0 {
		t.Fatalf("reserved bytes after release = %d", used)
	}
}

func TestReadRequestRejectsBodyAboveLimit(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	body := `{"model":"gpt-test"}`
	limit := int64(len(body) - 1)
	server.WithRequestBodyLimits(limit, limit*maxHTTPRequestBodyCopies, time.Minute)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))

	_, _, _, ok := server.readRequest(recorder, request)
	if ok || recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ok=%t status=%d body=%s", ok, recorder.Code, recorder.Body.String())
	}
	if used := server.requestBodyBudget.used.Load(); used != 0 {
		t.Fatalf("reserved bytes = %d", used)
	}
}

func TestReadRequestRejectsChunkedBodyAboveLimit(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	body := `{"model":"gpt-test"}`
	limit := int64(len(body) - 1)
	server.WithRequestBodyLimits(limit, limit*maxHTTPRequestBodyCopies, time.Minute)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	request.ContentLength = -1

	_, _, _, ok := server.readRequest(recorder, request)
	if ok || recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("ok=%t status=%d body=%s", ok, recorder.Code, recorder.Body.String())
	}
	if used := server.requestBodyBudget.used.Load(); used != 0 {
		t.Fatalf("reserved bytes = %d", used)
	}
}

func TestReadRequestRejectsWhenInflightCapacityIsUnavailable(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	body := `{"model":"gpt-test"}`
	server.WithRequestBodyLimits(64, 64*maxHTTPRequestBodyCopies, time.Minute)
	if !server.requestBodyBudget.tryAcquire(server.requestBodyBudget.limit) {
		t.Fatal("failed to occupy request body budget")
	}
	defer server.requestBodyBudget.release(server.requestBodyBudget.limit)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))

	_, _, _, ok := server.readRequest(recorder, request)
	if ok || recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "1" {
		t.Fatalf("ok=%t status=%d retry_after=%q body=%s", ok, recorder.Code, recorder.Header().Get("Retry-After"), recorder.Body.String())
	}
}

func TestByteBudgetEnforcesConcurrentCapacity(t *testing.T) {
	const (
		workers = 64
		limit   = 8
	)
	budget := newByteBudget(limit)
	release := make(chan struct{})
	results := make(chan bool, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			acquired := budget.tryAcquire(1)
			results <- acquired
			if acquired {
				<-release
				budget.release(1)
			}
		}()
	}
	var acquired atomic.Int64
	for range workers {
		if <-results {
			acquired.Add(1)
		}
	}
	if got := acquired.Load(); got != limit {
		t.Fatalf("concurrent acquisitions = %d, want %d", got, limit)
	}
	close(release)
	group.Wait()
	if used := budget.used.Load(); used != 0 {
		t.Fatalf("reserved bytes after release = %d", used)
	}
}

func TestUnknownLengthBodiesReserveCapacityIncrementally(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	const limit = int64(256 << 10)
	server.WithRequestBodyLimits(limit, limit*maxHTTPRequestBodyCopies, time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	firstResult := make(chan error, 1)
	go func() {
		_, reservation, err := server.readBufferedBody(&blockingBodyReader{
			reader: strings.NewReader(strings.Repeat("a", 1024)), started: started, release: release,
		}, -1, responsesWSRequestBodyCopies)
		if reservation != nil {
			reservation.release()
		}
		firstResult <- err
	}()
	<-started
	_, secondReservation, err := server.readBufferedBody(strings.NewReader(strings.Repeat("b", 1024)), -1, responsesWSRequestBodyCopies)
	if err != nil {
		close(release)
		t.Fatalf("second small body was rejected while capacity was available: %v", err)
	}
	secondReservation.release()
	close(release)
	if err = <-firstResult; err != nil {
		t.Fatalf("first body failed: %v", err)
	}
	if used := server.requestBodyBudget.used.Load(); used != 0 {
		t.Fatalf("reserved bytes after concurrent reads = %d", used)
	}
}

func TestResponsesAcceptsBodyAboveLegacyLimit(t *testing.T) {
	server, _, provider, plain := newTestServer(t)
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-large\",\"output\":[],\"usage\":{}}}\n\n")}
	body := `{"model":"gpt-test","input":[{"role":"user","content":[{"type":"input_text","text":"` + strings.Repeat("a", 33<<20) + `"}]}]}`
	recorder := serveGateway(t, server, plain, "/v1/responses", body)
	if recorder.Code != http.StatusOK || len(provider.bodies) != 1 {
		t.Fatalf("status=%d provider_calls=%d body=%s", recorder.Code, len(provider.bodies), recorder.Body.String())
	}
	if used := server.requestBodyBudget.used.Load(); used != 0 {
		t.Fatalf("reserved bytes after request = %d", used)
	}
}
