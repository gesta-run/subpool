package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

type copilotFixture struct {
	responses bool
	chat      bool
	body      io.ReadCloser
}

func (fixture copilotFixture) SupportsEndpoint(_ context.Context, _ string, endpoint string, _ copilot.Credentials) (bool, error) {
	if endpoint == copilot.EndpointResponses {
		return fixture.responses, nil
	}
	return fixture.chat, nil
}

func (fixture copilotFixture) ChatCompletions(context.Context, []byte, http.Header, copilot.Credentials) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: fixture.body}, nil
}

func (fixture copilotFixture) Responses(_ context.Context, body []byte, _ http.Header, _ copilot.Credentials) (*http.Response, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	tool := request["tools"].([]any)[0].(map[string]any)
	response := map[string]any{
		"id": "resp_illustrative", "object": "response", "status": "completed", "model": "illustrative-model",
		"output": []any{map[string]any{
			"type": "function_call", "id": "fc_illustrative", "call_id": "call_illustrative",
			"name": tool["name"], "arguments": `{"input":"ping"}`, "status": "completed",
		}},
		"usage": map[string]any{"input_tokens": 2, "output_tokens": 3},
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}

func TestInterleavedResponsesKeepTheirOwnToolMappings(t *testing.T) {
	provider, err := For(domain.ProviderCopilot, Clients{Copilot: copilotFixture{responses: true}})
	if err != nil {
		t.Fatal(err)
	}
	var responses []*Response
	for _, namespace := range []string{"workspace", "diagnostic"} {
		body := []byte(`{"input":"ping","tools":[{"type":"namespace","name":"` + namespace + `","tools":[{"type":"custom","name":"emit"}]}]}`)
		original := string(body)
		response, err := provider.RoundTrip(context.Background(), Request{Kind: "responses", Model: "illustrative-model", Body: body}, nil, Credentials{}, Options{AllowToolFallback: true})
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != original {
			t.Fatal("the adapter changed the caller's request buffer")
		}
		responses = append(responses, response)
	}
	for index := len(responses) - 1; index >= 0; index-- {
		response := responses[index]
		recorder := httptest.NewRecorder()
		usageCalls, sessionCalls := 0, 0
		response.Render(recorder, false, Request{Kind: "responses", Model: "illustrative-model"}, Observer{
			Usage: func(id, model string, input, output int64) {
				usageCalls++
				if id != "resp_illustrative" || model != "illustrative-model" || input != 2 || output != 3 {
					t.Fatalf("incorrect usage observation: %q %q %d %d", id, model, input, output)
				}
			},
			Session: func(id string) { sessionCalls++ },
		})
		response.Body.Close()
		var result map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		tool := result["output"].([]any)[0].(map[string]any)
		wantNamespace := []string{"workspace", "diagnostic"}[index]
		if recorder.Code != http.StatusOK || tool["type"] != "custom_tool_call" || tool["name"] != "emit" || tool["namespace"] != wantNamespace || tool["call_id"] != "call_illustrative" {
			t.Fatalf("the response used another request's tool mapping: %s", recorder.Body.String())
		}
		if usageCalls != 1 || sessionCalls != 1 {
			t.Fatalf("usage/session observed %d/%d times", usageCalls, sessionCalls)
		}
		for name := range recorder.Header() {
			if strings.Contains(strings.ToLower(name), "subpool-internal") {
				t.Fatalf("adapter metadata leaked in header %q", name)
			}
		}
	}
}

type blockingBody struct {
	closed   chan struct{}
	finished chan struct{}
	once     sync.Once
}

func (body *blockingBody) Read([]byte) (int, error) {
	<-body.closed
	close(body.finished)
	return 0, io.EOF
}

func (body *blockingBody) Close() error {
	body.once.Do(func() { close(body.closed) })
	return nil
}

func TestClosingBridgeStreamClosesItsUpstream(t *testing.T) {
	body := &blockingBody{closed: make(chan struct{}), finished: make(chan struct{})}
	provider, err := For(domain.ProviderCopilot, Clients{Copilot: copilotFixture{chat: true, body: body}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.RoundTrip(context.Background(), Request{Kind: "responses", Model: "illustrative-model", Body: []byte(`{"input":"ping","stream":true}`)}, nil, Credentials{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	stream := response.StreamHTTP("illustrative-model", Observer{})
	if err := stream.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-body.finished:
	case <-time.After(time.Second):
		t.Fatal("the adapter left its upstream reader blocked after the caller closed the stream")
	}
}

func TestUnsupportedCopilotEndpointIsDistinctFromInvalidRequest(t *testing.T) {
	provider, err := For(domain.ProviderCopilot, Clients{Copilot: copilotFixture{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.RoundTrip(context.Background(), Request{Kind: "responses", Model: "illustrative-model", Body: []byte(`{"input":"ping"}`)}, nil, Credentials{}, Options{})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported endpoint error = %v", err)
	}
	provider, err = For(domain.ProviderCopilot, Clients{Copilot: copilotFixture{responses: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.RoundTrip(context.Background(), Request{Kind: "responses", Model: "gemini-3.8-flash", Body: []byte(`{"reasoning":{"effort":"xhigh"}}`)}, nil, Credentials{}, Options{})
	var invalid *RequestError
	if !errors.As(err, &invalid) || invalid.Code != "invalid_reasoning_effort" {
		t.Fatalf("invalid request lost its error code: %v", err)
	}
}
