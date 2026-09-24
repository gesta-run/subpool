package responsesws

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/domain"
)

func TestSessionPublishesContinuationBeforeFinishing(t *testing.T) {
	session := &responsesWSSession{responses: make(map[string]struct{})}
	turn := &responsesWSTurn{accountID: "account-1"}
	session.observeTurn(turn, []byte(`{"type":"response.completed","response":{"id":"resp-local"}}`), "response.completed")
	if requestErr := session.validateContinuation("resp-local"); requestErr != nil {
		t.Fatalf("continuation error = %#v", requestErr)
	}
}

func TestParseRequestRejectsDuplicateControlField(t *testing.T) {
	_, requestErr := parseResponsesWSRequest([]byte(`{"type":"response.create","previous_response_id":"first","previous_response_id":"second"}`))
	if requestErr == nil || !strings.Contains(requestErr.Message, "duplicate previous_response_id") {
		t.Fatalf("request error = %#v", requestErr)
	}
}

func TestParseRequestValidatesStreamID(t *testing.T) {
	for _, streamID := range []string{"", "has space", "worker/1", "任务"} {
		payload, _ := json.Marshal(map[string]any{"type": "response.create", "stream_id": streamID})
		if _, requestErr := parseResponsesWSRequest(payload); requestErr == nil || requestErr.Code != "invalid_stream_id" {
			t.Fatalf("stream_id %q was accepted", streamID)
		}
	}
	valid := strings.Repeat("a", responsesWSMaxStreamIDBytes)
	payload, _ := json.Marshal(map[string]any{"type": "response.create", "stream_id": valid})
	if _, requestErr := parseResponsesWSRequest(payload); requestErr != nil {
		t.Fatalf("valid stream_id was rejected: %#v", requestErr)
	}
}

func TestClassifyLimitEvent(t *testing.T) {
	for _, payload := range []string{
		`{"type":"error","error":{"code":"usage_limit_reached"}}`,
		`{"type":"error","error":{"message":"You have hit your usage limit. Please try again later."}}`,
	} {
		if classifyResponsesWSLimitEvent([]byte(payload)) != responsesWSLimitQuota {
			t.Fatalf("usage limit event was not recognized: %s", payload)
		}
	}
	if classifyResponsesWSLimitEvent([]byte(`{"type":"response.failed","response":{"error":{"type":"rate_limit_exceeded"}}}`)) != responsesWSLimitTemporary {
		t.Fatal("temporary rate limit was not recognized")
	}
	if classifyResponsesWSLimitEvent([]byte(`{"type":"error","error":{"code":"invalid_request_error","message":"Invalid input"}}`)) != responsesWSLimitNone {
		t.Fatal("invalid request was classified as a usage limit")
	}
}

func TestSessionRejectsTurnDuringNativeFailover(t *testing.T) {
	session := &responsesWSSession{
		hub: &Hub{}, account: domain.ProviderAccount{ID: "account-1"},
		native: true, nativeSwitching: true, streams: make(map[string]*responsesWSStream), named: make(map[string]struct{}),
	}
	request, requestErr := parseResponsesWSRequest([]byte(`{"type":"response.create","model":"gpt-test","input":"hello"}`))
	if requestErr != nil {
		t.Fatalf("parse error = %#v", requestErr)
	}
	if _, reserveErr := session.reserveTurn(request, request.raw, nil); reserveErr == nil || reserveErr.Code != "provider_error" {
		t.Fatalf("reserve error = %#v", reserveErr)
	}
}
