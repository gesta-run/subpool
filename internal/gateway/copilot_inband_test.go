package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/gesta-run/subpool/internal/adapter/copilotcompact"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

func newInbandTest(t *testing.T, native bool) (*Server, *fakeStore, *compactCopilotProvider, string) {
	t.Helper()
	server, st, _, key := newTestServer(t)
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &compactCopilotProvider{}
	provider.supportedEndpoints = map[string]map[string]bool{"gpt-test": {copilot.EndpointResponses: native, copilot.EndpointChatCompletions: !native}}
	return server.WithCopilot(provider), st, provider, key
}

func inbandOutput(t *testing.T, body string, stream bool) json.RawMessage {
	t.Helper()
	if stream {
		for _, line := range strings.Split(body, "\n") {
			var event struct {
				Type     string
				Response json.RawMessage
			}
			if strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event.Type == "response.completed" {
				body = string(event.Response)
				break
			}
		}
	}
	var result struct {
		Status string
		Output []json.RawMessage
	}
	if json.Unmarshal([]byte(body), &result) != nil || result.Status != "completed" || len(result.Output) != 1 {
		t.Fatalf("missing single completed compaction output: %.500s", body)
	}
	var item struct{ Type, EncryptedContent string }
	_ = json.Unmarshal(result.Output[0], &item)
	if item.Type != "compaction" || !strings.Contains(string(result.Output[0]), `"encrypted_content":"subpool-copilot-v1.`) {
		t.Fatalf("invalid opaque item: %.300s", result.Output[0])
	}
	return result.Output[0]
}

func TestCopilotInbandCompactionContinuesAndCompactsAgain(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("native=%t/stream=%t", native, stream), func(t *testing.T) {
				server, st, provider, key := newInbandTest(t, native)
				provider.maxBytes = copilotcompact.ChunkBytes / 2
				prefix := `{"role":"developer","content":"Illustrative constraint"},{"role":"user","content":"Illustrative initial task"},{"role":"user","content":"Illustrative latest task"}`
				body := fmt.Sprintf(`{"model":"gpt-test","stream":%t,"input":[%s,{"role":"assistant","content":%q},{"type":"compaction_trigger","id":"trigger_fixture"}]}`, stream, prefix, strings.Repeat("illustrative completed history ", 9000))
				recorder := serveGateway(t, server, key, "/v1/responses", body)
				if recorder.Code != 200 || st.sessionSaved || st.usageCalls != 1 || st.usageInput != int64(provider.successes)*5 || provider.successes < 2 {
					t.Fatalf("compaction status=%d successes=%d usage=%d calls=%d session=%v body=%.500s", recorder.Code, provider.successes, st.usageInput, st.usageCalls, st.sessionSaved, recorder.Body.String())
				}
				item := inbandOutput(t, recorder.Body.String(), stream)
				continued := serveGateway(t, server, key, "/v1/responses", fmt.Sprintf(`{"model":"gpt-test","input":[%s,%s,{"role":"user","content":"Illustrative next turn"}]}`, prefix, item))
				if continued.Code != 200 || !st.sessionSaved {
					t.Fatalf("state could not continue: %d %.500s", continued.Code, continued.Body.String())
				}
				upstream := string(provider.requests[len(provider.requests)-1])
				for _, text := range []string{"Illustrative constraint", "Illustrative initial task", "Illustrative latest task", "Illustrative next turn", "Illustrative recovered task summary"} {
					if strings.Count(upstream, text) != 1 {
						t.Fatalf("missing or duplicated restored context %q: %.1000s", text, upstream)
					}
				}
				repeated := serveGateway(t, server, key, "/v1/responses", fmt.Sprintf(`{"model":"gpt-test","input":[%s,%s,{"role":"user","content":"Illustrative next turn"},{"role":"assistant","content":"Additional history"},{"type":"compaction_trigger"}]}`, prefix, item))
				if repeated.Code != 200 {
					t.Fatalf("repeated compaction failed: %d %.500s", repeated.Code, repeated.Body.String())
				}
				second := inbandOutput(t, repeated.Body.String(), false)
				last := serveGateway(t, server, key, "/v1/responses", fmt.Sprintf(`{"model":"gpt-test","input":[%s,{"role":"user","content":"Continue after second compaction"}]}`, second))
				if last.Code != 200 || server.requestBodyBudget.used.Load() != 0 {
					t.Fatalf("second window failed or leaked reservation: %d", last.Code)
				}
				for _, raw := range provider.requests {
					if strings.Contains(string(raw), "subpool-copilot-v1.") || strings.Contains(string(raw), `"type":"compaction_trigger"`) || strings.Contains(string(raw), `"type":"compaction"`) {
						t.Fatal("Subpool protocol items leaked to Copilot")
					}
				}
			})
		}
	}
}

func TestCopilotInbandCompactionPreservesPendingToolGroup(t *testing.T) {
	for _, native := range []bool{false, true} {
		server, _, provider, key := newInbandTest(t, native)
		request := `{"model":"gpt-test","input":[{"role":"user","content":"Illustrative task"},{"role":"assistant","content":"Earlier history"},{"type":"function_call","name":"fixture","call_id":"call_a","arguments":"{}"},{"type":"function_call","name":"fixture","call_id":"call_b","arguments":"{}"},{"type":"function_call_output","call_id":"call_a","output":"completed parallel call"},{"type":"compaction_trigger"}]}`
		recorder := serveGateway(t, server, key, "/v1/responses", request)
		if recorder.Code != 200 {
			t.Fatalf("pending group could not compact: %d %s", recorder.Code, recorder.Body.String())
		}
		item := inbandOutput(t, recorder.Body.String(), false)
		continued := serveGateway(t, server, key, "/v1/responses", fmt.Sprintf(`{"model":"gpt-test","input":[%s,{"type":"function_call_output","call_id":"call_b","output":"remaining parallel result"}],"tools":[{"type":"function","name":"fixture","parameters":{"type":"object"}}]}`, item))
		if continued.Code != 200 {
			t.Fatalf("pending tool continuation failed: %d %s", continued.Code, continued.Body.String())
		}
		upstream := string(provider.requests[len(provider.requests)-1])
		for _, value := range []string{"call_a", "call_b", "completed parallel call", "remaining parallel result"} {
			if !strings.Contains(upstream, value) {
				t.Fatalf("pending tool group lost %q", value)
			}
		}
	}
}

func TestCopilotInbandRejectsUnsafeInputsWithoutInference(t *testing.T) {
	for _, extra := range []string{
		`"previous_response_id":"resp_fixture",`,
		`"previous_response_id":"resp_subpool_cmp_fixture",`,
		`"context_management":[{"type":"compaction"}],`,
	} {
		server, st, provider, key := newInbandTest(t, true)
		st.sessionAccount = st.route.Account
		recorder := serveGateway(t, server, key, "/v1/responses", `{`+extra+`"model":"gpt-test","input":[{"role":"user","content":"fixture"},{"type":"compaction_trigger"}]}`)
		if recorder.Code != 400 || provider.responsesCalls+provider.chatCalls != 0 {
			t.Fatalf("unsafe input performed inference: %d %.300s", recorder.Code, recorder.Body.String())
		}
	}
	for _, input := range []string{
		`[{"type":"compaction_trigger"},{"role":"user","content":"fixture"}]`,
		`[{"type":"compaction","id":"cmp_subpool_fixture","encrypted_content":"invalid"},{"role":"user","content":"fixture"}]`,
		`[{"type":"compaction","id":"cmp_foreign","encrypted_content":"foreign"},{"role":"user","content":"fixture"}]`,
	} {
		server, st, provider, key := newInbandTest(t, true)
		recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"gpt-test","input":`+input+`}`)
		if recorder.Code != 400 || provider.responsesCalls+provider.chatCalls != 0 || st.usageCalls != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("invalid state performed inference or leaked buffers: %d %.300s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestCopilotInbandPartialFailureDoesNotReplayOrInstallState(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		server, st, provider, key := newInbandTest(t, true)
		st.route.Pool.Provider = domain.ProviderMixed
		if incomplete {
			provider.incompleteAfter = 1
		} else {
			provider.failureAfter = 1
		}
		request := fmt.Sprintf(`{"model":"gpt-test","input":[{"role":"user","content":"fixture"},{"role":"assistant","content":%q},{"type":"compaction_trigger"}]}`, strings.Repeat("illustrative history ", 16000))
		recorder := serveGateway(t, server, key, "/v1/responses", request)
		if recorder.Code != 502 || st.sessionSaved || st.usageCalls != 1 || provider.responsesCalls != 2 || len(st.reassignExcludes) != 0 || strings.Contains(recorder.Body.String(), "encrypted_content") || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("partial summary became live or replayed: status=%d usage=%d calls=%d body=%.300s", recorder.Code, st.usageCalls, provider.responsesCalls, recorder.Body.String())
		}
	}
}

func readInbandWSTerminal(t *testing.T, client *websocket.Conn) map[string]any {
	t.Helper()
	for index := 0; index < 20; index++ {
		event := readResponsesWSMessage(t, client)
		if event["type"] == "error" {
			t.Fatalf("WebSocket compaction failed: %#v", event)
		}
		if event["type"] == "response.completed" {
			return event
		}
	}
	t.Fatal("no terminal compaction event")
	return nil
}

func TestCopilotInbandWebSocketUsageAndContinuation(t *testing.T) {
	for _, native := range []bool{false, true} {
		server, st, provider, key := newInbandTest(t, native)
		server.WithResponsesWebSocket(true, false, "")
		client, cleanup := dialResponsesWSTestServer(t, server, key)
		t.Cleanup(cleanup)
		writeResponsesWSMessage(t, client, `{"type":"response.create","stream_id":"fixture","model":"gpt-test","input":[{"role":"user","content":"Illustrative task"},{"role":"assistant","content":"Illustrative history"},{"type":"compaction_trigger"}]}`)
		event := readInbandWSTerminal(t, client)
		if event["stream_id"] != "fixture" {
			t.Fatal("lost the WebSocket stream ID")
		}
		raw, _ := json.Marshal(event["response"])
		item := inbandOutput(t, string(raw), false)
		writeResponsesWSMessage(t, client, fmt.Sprintf(`{"type":"response.create","stream_id":"fixture","model":"gpt-test","input":[%s,{"role":"user","content":"Continue fixture"}]}`, item))
		_ = readInbandWSTerminal(t, client)
		cleanup()
		if st.usageCalls != 2 || st.usageInput != 10 || st.usageOutput != 4 || st.sessionCalls != 1 || provider.responsesCalls+provider.chatCalls != 2 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("WebSocket duplicated compaction usage/session or leaked reservation: usage=%d/%d calls=%d sessions=%d", st.usageInput, st.usageOutput, st.usageCalls, st.sessionCalls)
		}
	}
}

func TestSubpoolStateRestoresBeforeSwitchingProviderOrWireProtocol(t *testing.T) {
	for _, ws := range []bool{false, true} {
		server, st, provider, key := newTestServer(t)
		state := &copilotcompact.State{Cipher: server.cipher, Scope: st.route.Key.ID}
		item, err := state.Seal([]json.RawMessage{json.RawMessage(`{"role":"user","content":"Illustrative retained task"}`)}, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		provider.responses = append(provider.responses, sseResponse(200, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-fixture\",\"status\":\"completed\",\"output\":[],\"usage\":{}}}\n\n"))
		if ws {
			// A Subpool token must use the bridge, not the native Codex socket.
			server.WithResponsesWebSocket(true, false, "http://127.0.0.1:1")
			client, cleanup := dialResponsesWSTestServer(t, server, key)
			t.Cleanup(cleanup)
			writeResponsesWSMessage(t, client, fmt.Sprintf(`{"type":"response.create","model":"gpt-test","input":[%s]}`, item))
			_ = readInbandWSTerminal(t, client)
			cleanup()
		} else {
			recorder := serveGateway(t, server, key, "/v1/responses", fmt.Sprintf(`{"model":"gpt-test","input":[%s]}`, item))
			if recorder.Code != 200 {
				t.Fatalf("cross-provider replay failed: %d %s", recorder.Code, recorder.Body.String())
			}
		}
		if len(provider.bodies) != 1 || strings.Contains(string(provider.bodies[0]), "encrypted_content") || !strings.Contains(string(provider.bodies[0]), "Illustrative retained task") {
			t.Fatal("Subpool state was not safely restored before provider switching")
		}
	}
}

func TestCopilotInbandCapacityAndNoop(t *testing.T) {
	server, st, provider, key := newInbandTest(t, true)
	body := `{"model":"gpt-test","input":[{"role":"user","content":"Illustrative task"},{"type":"compaction_trigger"}]}`
	recorder := serveGateway(t, server, key, "/v1/responses", body)
	if recorder.Code != 200 || st.usageCalls != 0 || st.sessionCalls != 0 || provider.responsesCalls != 0 {
		t.Fatalf("unchanged short window performed inference: %d %s", recorder.Code, recorder.Body.String())
	}
	_ = inbandOutput(t, recorder.Body.String(), false)
	var result struct{ Output []json.RawMessage }
	_ = json.Unmarshal(recorder.Body.Bytes(), &result)
	st.sessionAccount = st.route.Account
	continued := serveGateway(t, server, key, "/v1/responses", fmt.Sprintf(`{"model":"gpt-test","previous_response_id":"resp_fixture","input":[%s]}`, result.Output[0]))
	if continued.Code != 400 || !strings.Contains(continued.Body.String(), "previous_response_not_found") || provider.responsesCalls != 0 {
		t.Fatal("opaque state was combined with incompatible upstream continuation")
	}
	server.WithRequestBodyLimits(int64(len(body)), int64(len(body))*maxHTTPRequestBodyCopies, time.Minute)
	recorder = serveGateway(t, server, key, "/v1/responses", body)
	if recorder.Code != 503 || !strings.Contains(recorder.Body.String(), "subpool_capacity_exceeded") || provider.responsesCalls != 0 || len(st.healthFailureCodes) != 0 || server.requestBodyBudget.used.Load() != 0 {
		t.Fatalf("in-band work bypassed admission: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCopilotInbandOversizedRetainedWindowFailsBeforeInference(t *testing.T) {
	server, st, provider, key := newInbandTest(t, true)
	request := fmt.Sprintf(`{"model":"gpt-test","input":[{"role":"developer","content":%q},{"role":"user","content":"Fixture task"},{"role":"assistant","content":"Fixture history"},{"type":"compaction_trigger"}]}`, strings.Repeat("x", copilotcompact.MaxStateBytes))
	recorder := serveGateway(t, server, key, "/v1/responses", request)
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "unsupported_compaction") || st.usageCalls != 0 || provider.responsesCalls != 0 || server.requestBodyBudget.used.Load() != 0 {
		t.Fatalf("oversized window performed inference: %d %.300s", recorder.Code, recorder.Body.String())
	}
}

func TestCopilotInbandRejectsWorstCaseStateBeforeInference(t *testing.T) {
	for _, stream := range []bool{false, true} {
		server, st, provider, key := newInbandTest(t, true)
		body := fmt.Sprintf(`{"model":"gpt-test","stream":%t,"input":[{"role":"user","content":%q},{"role":"assistant","content":"Illustrative history"},{"type":"compaction_trigger"}]}`, stream, strings.Repeat("x", 940<<10))
		response := serveGateway(t, server, key, "/v1/responses", body)
		if response.Code != 400 || !strings.Contains(response.Body.String(), "unsupported_compaction") || provider.responsesCalls != 0 || st.usageCalls != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("impossible state incurred inference or leaked buffers: status=%d calls=%d usage=%d", response.Code, provider.responsesCalls, st.usageCalls)
		}
	}
}

func readInbandHTTPEvent(t *testing.T, scanner *bufio.Scanner) map[string]any {
	t.Helper()
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(scanner.Text(), "data: ")), &event) != nil {
				t.Fatal("invalid SSE event")
			}
			return event
		}
	}
	t.Fatalf("missing SSE event: %v", scanner.Err())
	return nil
}

func TestCopilotInbandHTTPStreamsBeforeSummaryAndKeepsAdmission(t *testing.T) {
	for _, native := range []bool{false, true} {
		server, st, provider, key := newInbandTest(t, native)
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		provider.bodyGate = gate
		mux := http.NewServeMux()
		server.Register(mux)
		upstream := httptest.NewServer(mux)
		t.Cleanup(upstream.Close)
		t.Cleanup(release)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)
		body := `{"model":"gpt-test","stream":true,"input":[{"role":"user","content":"Illustrative task"},{"role":"assistant","content":"Illustrative history"},{"type":"compaction_trigger"}]}`
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/v1/responses", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("no headers while summary was pending: %v", err)
		}
		t.Cleanup(func() { response.Body.Close() })
		scanner := bufio.NewScanner(response.Body)
		for _, kind := range []string{"response.created", "response.in_progress", "response.output_item.added"} {
			if event := readInbandHTTPEvent(t, scanner); event["type"] != kind {
				t.Fatalf("unexpected initial lifecycle: %#v", event)
			}
		}
		if used := server.requestBodyBudget.used.Load(); used != int64(len(body))*compactHTTPRequestBodyCopies {
			t.Fatalf("work buffers were released before summary completion: %d", used)
		}
		release()
		for readInbandHTTPEvent(t, scanner)["type"] != "response.completed" {
		}
		for scanner.Scan() {
		}
		response.Body.Close()
		upstream.Close()
		if st.usageCalls != 1 || st.sessionCalls != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("stream duplicated usage/session or leaked admission: usage=%d sessions=%d buffers=%d", st.usageCalls, st.sessionCalls, server.requestBodyBudget.used.Load())
		}
	}
}

func TestCopilotInbandStreamingFailureAndDisconnectReleaseAdmission(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		server, st, provider, key := newInbandTest(t, true)
		st.route.Pool.Provider = domain.ProviderMixed
		provider.failureAfter = 1
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		provider.bodyGate = gate
		mux := http.NewServeMux()
		server.Register(mux)
		upstream := httptest.NewServer(mux)
		t.Cleanup(upstream.Close)
		t.Cleanup(release)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		t.Cleanup(cancel)
		body := fmt.Sprintf(`{"model":"gpt-test","stream":true,"input":[{"role":"user","content":"Illustrative task"},{"role":"assistant","content":%q},{"type":"compaction_trigger"}]}`, strings.Repeat("x", 300<<10))
		request, _ := http.NewRequestWithContext(ctx, http.MethodPost, upstream.URL+"/v1/responses", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { response.Body.Close() })
		if !disconnect {
			scanner := bufio.NewScanner(response.Body)
			for index := 0; index < 3; index++ {
				_ = readInbandHTTPEvent(t, scanner)
			}
			release()
			event := readInbandHTTPEvent(t, scanner)
			raw, _ := json.Marshal(event)
			if event["type"] != "response.failed" || strings.Contains(string(raw), "encrypted_content") || !strings.Contains(string(raw), "compaction_interrupted") {
				t.Fatalf("partial failure installed state: %s", raw)
			}
			_, _ = io.Copy(io.Discard, response.Body)
		}
		cancel()
		response.Body.Close()
		upstream.Close()
		wantUsage := 1
		if disconnect {
			wantUsage = 0
		}
		if st.usageCalls != wantUsage || st.sessionCalls != 0 || len(st.reassignExcludes) != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatalf("cancel/failure replayed inference or leaked buffers: usage=%d sessions=%d buffers=%d", st.usageCalls, st.sessionCalls, server.requestBodyBudget.used.Load())
		}
	}
}

func TestCopilotInbandWebSocketStreamsBeforeSummary(t *testing.T) {
	for _, native := range []bool{false, true} {
		server, st, provider, key := newInbandTest(t, native)
		server.WithResponsesWebSocket(true, false, "")
		gate := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		provider.bodyGate = gate
		client, cleanup := dialResponsesWSTestServer(t, server, key)
		t.Cleanup(cleanup)
		t.Cleanup(release)
		body := `{"type":"response.create","model":"gpt-test","input":[{"role":"user","content":"Illustrative task"},{"role":"assistant","content":"Illustrative history"},{"type":"compaction_trigger"}]}`
		writeResponsesWSMessage(t, client, body)
		for _, kind := range []string{"response.created", "response.in_progress", "response.output_item.added"} {
			if event := readResponsesWSMessage(t, client); event["type"] != kind {
				t.Fatalf("WebSocket waited for summary completion: %#v", event)
			}
		}
		if server.requestBodyBudget.used.Load() < int64(len(body))*compactHTTPRequestBodyCopies {
			t.Fatal("WebSocket released its work reservation before inference completed")
		}
		release()
		_ = readInbandWSTerminal(t, client)
		cleanup()
		if st.usageCalls != 1 || st.sessionCalls != 0 || server.requestBodyBudget.used.Load() != 0 {
			t.Fatal("WebSocket leaked work reservation or duplicated accounting")
		}
	}
}
