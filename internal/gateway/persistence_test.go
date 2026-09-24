package gateway

import (
	"bytes"
	"errors"
	"net/http"
	"testing"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
)

func TestUsageAndSessionWritesRetry(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.usageFailures = 2
	st.sessionFailures = 2
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-retry\",\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","stream":true,"input":"hello"}`)
	if recorder.Code != http.StatusOK || st.usageCalls != 3 || st.sessionCalls != 3 || st.usageInput != 5 || !st.sessionSaved {
		t.Fatalf("response=%d usage calls=%d session calls=%d usage=%d/%d saved=%v", recorder.Code, st.usageCalls, st.sessionCalls, st.usageInput, st.usageOutput, st.sessionSaved)
	}
}

func TestUsageRetryDoesNotDuplicateCommittedEvent(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.usageCommitUncertain = true
	provider.responses = []*http.Response{sseResponse(http.StatusOK, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":2}}}\n\n")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","stream":true,"input":"hello"}`)
	if recorder.Code != http.StatusOK || st.usageCalls != 2 || st.usageInput != 5 || st.usageOutput != 2 || len(st.usageEvents) != 1 {
		t.Fatalf("response=%d calls=%d usage=%d/%d events=%d", recorder.Code, st.usageCalls, st.usageInput, st.usageOutput, len(st.usageEvents))
	}
}

func TestResponseIDProducesStableUsageEventHash(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	fallback := server.randomUsageEventHash()
	first := server.usageEventHash("resp-stable", fallback)
	second := server.usageEventHash("resp-stable", server.randomUsageEventHash())
	if !bytes.Equal(first, second) || bytes.Contains(first, []byte("resp-stable")) {
		t.Fatal("usage event hashes are not stable and irreversible")
	}
}

func TestTransientRefreshFailureCoolsAccount(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	server.refresher.(*fakeRefresher).err = errors.New("network unavailable")
	provider.responses = []*http.Response{sseResponse(http.StatusUnauthorized, "unauthorized")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusServiceUnavailable || len(st.status) == 0 || st.status[0] != domain.AccountCoolingDown {
		t.Fatalf("response=%d statuses=%#v body=%s", recorder.Code, st.status, recorder.Body.String())
	}
}

func TestDefinitiveRefreshFailureMarksAuthFailed(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	server.refresher.(*fakeRefresher).err = &codex.TokenError{StatusCode: http.StatusBadRequest, Code: "invalid_grant"}
	provider.responses = []*http.Response{sseResponse(http.StatusUnauthorized, "unauthorized")}
	recorder := serveGateway(t, server, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if recorder.Code != http.StatusUnauthorized || len(st.status) == 0 || st.status[0] != domain.AccountAuthFailed {
		t.Fatalf("response=%d statuses=%#v body=%s", recorder.Code, st.status, recorder.Body.String())
	}
}
