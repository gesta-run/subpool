package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

const nativeCopilotTestTools = `[
	{"type":"function","name":"workspace__lookup","parameters":{"type":"object"}},
	{"type":"namespace","name":"workspace","tools":[
		{"type":"function","name":"lookup","parameters":{"type":"object"}},
		{"type":"custom","name":"apply_patch","description":"Apply an illustrative patch"}
	]},
	{"type":"web_search"}
]`

func TestCopilotNativeResponsesNormalizesCLITools(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{
		"grok-test": {copilot.EndpointResponses: true},
	}}
	request := `{"model":"grok-test","instructions":"Keep the context","input":"hello","tools":` + nativeCopilotTestTools + `,"tool_choice":"auto","reasoning":{"effort":"high","summary":"auto"},"store":false,"include":["reasoning.encrypted_content"],"metadata":{"example":9007199254740993}}`
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", request)
	if recorder.Code != http.StatusOK || provider.responsesCalls != 1 || provider.chatCalls != 0 {
		t.Fatalf("status=%d calls=%d/%d body=%s", recorder.Code, provider.responsesCalls, provider.chatCalls, recorder.Body.String())
	}
	var upstream map[string]any
	if err := json.Unmarshal(provider.body, &upstream); err != nil {
		t.Fatal(err)
	}
	tools := upstream["tools"].([]any)
	if len(tools) != 3 || upstream["stream"] != true || upstream["instructions"] != "Keep the context" {
		t.Fatalf("upstream=%s", provider.body)
	}
	for _, entry := range tools {
		if entry.(map[string]any)["type"] != "function" {
			t.Fatalf("unsupported tool forwarded: %#v", entry)
		}
	}
	if !strings.Contains(string(provider.body), `"example":9007199254740993`) ||
		!strings.Contains(string(provider.body), `"reasoning":{"effort":"high","summary":"auto"}`) {
		t.Fatalf("native fields were changed: %s", provider.body)
	}
}

func TestCopilotNativeResponsesToolHistoryRoundTrip(t *testing.T) {
	request := `{"model":"grok-test","previous_response_id":"resp-previous","tools":` + nativeCopilotTestTools + `,"input":[
		{"type":"reasoning","id":"reason-1","encrypted_content":"illustrative-opaque-data","summary":[]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}],"example":9007199254740993},
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","namespace":"workspace","arguments":"{}"},
		{"type":"function_call_output","call_id":"call-1","output":"illustrative result"},
		{"type":"custom_tool_call","id":"fc-2","call_id":"call-2","name":"apply_patch","namespace":"workspace","input":"line one\nline two"},
		{"type":"custom_tool_call_output","call_id":"call-2","output":"done"}
	],"tool_choice":{"type":"custom","name":"apply_patch","namespace":"workspace"}}`
	raw, mappings, err := normalizeCopilotNativeResponses([]byte(request), "grok-test", true)
	if err != nil {
		t.Fatal(err)
	}
	var upstream map[string]any
	if err := decodeCopilotNativeJSON(raw, &upstream); err != nil {
		t.Fatal(err)
	}
	lookup := copilotToolName("lookup", "workspace", mappings)
	if lookup == "workspace__lookup" || len(lookup) > maxCopilotToolNameLength {
		t.Fatalf("namespace name collision was not handled: %q", lookup)
	}
	items := upstream["input"].([]any)
	call := items[2].(map[string]any)
	if call["name"] != lookup || call["namespace"] != nil || call["id"] != "fc-1" || call["call_id"] != "call-1" {
		t.Fatalf("function history=%#v", call)
	}
	custom := items[4].(map[string]any)
	if custom["type"] != "function_call" || custom["name"] != stableCopilotNativeToolName("workspace", "apply_patch") || custom["input"] != nil ||
		customToolInput(custom["arguments"].(string)) != "line one\nline two" || custom["call_id"] != "call-2" ||
		items[5].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("custom history=%#v", items)
	}
	choice := upstream["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != custom["name"] || choice["namespace"] != nil || upstream["previous_response_id"] != "resp-previous" {
		t.Fatalf("tool choice or continuation=%s", raw)
	}
	if !strings.Contains(string(raw), `"example":9007199254740993`) || items[0].(map[string]any)["encrypted_content"] != "illustrative-opaque-data" {
		t.Fatalf("native reasoning or precision was lost: %s", raw)
	}
}

func TestCopilotNativeResponsesAdditionalAndDeferredTools(t *testing.T) {
	request := `{"tools":[{"type":"tool_search","execution":"client"}],"input":[
		{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"files","tools":[{"type":"function","name":"read","defer_loading":true,"parameters":{"type":"object"}}]}]},
		{"type":"message","role":"user","content":"read a file"}
	],"tool_choice":{"type":"tool_search"}}`
	raw, mappings, err := normalizeCopilotNativeResponses([]byte(request), "grok-test", true)
	if err != nil || len(mappings) != 1 || strings.Contains(string(raw), "tool_search") || strings.Contains(string(raw), "additional_tools") ||
		strings.Contains(string(raw), "defer_loading") || !strings.Contains(string(raw), "files__read") {
		t.Fatalf("request=%s mappings=%#v error=%v", raw, mappings, err)
	}
}

func TestCopilotNativeResponsesToolChoiceFiltering(t *testing.T) {
	lookupChoice := `"name":"` + stableCopilotNativeToolName("workspace", "lookup") + `"`
	tests := []struct {
		name, choice, want string
		wantError          bool
	}{
		{"auto", `"auto"`, `"auto"`, false},
		{"forced function", `{"type":"function","name":"lookup","namespace":"workspace"}`, lookupChoice, false},
		{"allowed tools", `{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"lookup","namespace":"workspace"},{"type":"web_search"}]}`, lookupChoice, false},
		{"empty allowlist", `{"type":"allowed_tools","mode":"auto","tools":[{"type":"web_search"}]}`, `"none"`, false},
		{"required empty allowlist", `{"type":"allowed_tools","mode":"required","tools":[{"type":"web_search"}]}`, "", true},
		{"forced hosted tool", `{"type":"web_search"}`, "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := `{"input":"hello","tools":[{"type":"namespace","name":"workspace","tools":[{"type":"function","name":"lookup"}]},{"type":"web_search"}],"tool_choice":` + test.choice + `}`
			raw, _, err := normalizeCopilotNativeResponses([]byte(request), "grok-test", true)
			if (err != nil) != test.wantError || err == nil && !strings.Contains(string(raw), test.want) {
				t.Fatalf("request=%s error=%v", raw, err)
			}
		})
	}
	for _, choice := range []string{`"auto"`, `"none"`, `"required"`} {
		request := `{"input":"hi","tools":[{"type":"web_search"}],"tool_choice":` + choice + `}`
		raw, _, err := normalizeCopilotNativeResponses([]byte(request), "grok-test", true)
		if choice == `"required"` {
			if err == nil {
				t.Fatal("required tool was silently discarded")
			}
		} else if choice == `"none"` {
			if err != nil || strings.Contains(string(raw), `"tools"`) || !strings.Contains(string(raw), `"tool_choice":"none"`) {
				t.Fatalf("explicit tool prohibition was lost: request=%s error=%v", raw, err)
			}
		} else if err != nil || strings.Contains(string(raw), "tools") || strings.Contains(string(raw), "tool_choice") {
			t.Fatalf("request=%s error=%v", raw, err)
		}
	}
}

func TestMixedPoolNativeCopilotToolFailurePreservesOriginalRequest(t *testing.T) {
	server, st, _, plain := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
	st.route.Pool.Provider = domain.ProviderMixed
	st.reassigned = compatibleAccountWithCipher(t, cipher, "compatible-account")
	compatible := &fakeCompatibleProvider{}
	server.compatible = compatible
	provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{
		"grok-test": {copilot.EndpointResponses: true},
	}}
	request := `{"model":"grok-test","input":"hello","tools":[{"type":"web_search"}]}`
	recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", request)
	var original, forwarded map[string]any
	_ = json.Unmarshal([]byte(request), &original)
	_ = json.Unmarshal(compatible.responsesBody, &forwarded)
	original["stream"] = true
	want, _ := json.Marshal(original)
	got, _ := json.Marshal(forwarded)
	if recorder.Code != http.StatusOK || provider.responsesCalls != 0 || !bytes.Equal(want, got) {
		t.Fatalf("status=%d calls=%d original=%s body=%s", recorder.Code, provider.responsesCalls, compatible.responsesBody, recorder.Body.String())
	}
}

func nativeCopilotToolResponseFixture() string {
	return strings.ReplaceAll(`event: response.created
data: {"type":"response.created","sequence_number":0,"response":{"id":"resp-native","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"reason-1","encrypted_content":"illustrative-opaque-data","summary":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"workspace__apply_patch","arguments":"","status":"in_progress"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":3,"item_id":"fc-1","output_index":1,"delta":"{\"input\":\"line one\\n"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","sequence_number":4,"item_id":"fc-1","output_index":1,"delta":"line two\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","sequence_number":5,"item_id":"fc-1","output_index":1,"arguments":"{\"input\":\"line one\\nline two\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","sequence_number":6,"output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"workspace__apply_patch","arguments":"{\"input\":\"line one\\nline two\"}","status":"completed"}}

event: response.completed
data: {"type":"response.completed","sequence_number":7,"response":{"id":"resp-native","status":"completed","model":"grok-test","output":[{"type":"reasoning","id":"reason-1","encrypted_content":"illustrative-opaque-data","summary":[]},{"type":"function_call","id":"fc-1","call_id":"call-1","name":"workspace__apply_patch","arguments":"{\"input\":\"line one\\nline two\"}","status":"completed"}],"usage":{"input_tokens":7,"output_tokens":3}}}

	`, "workspace__apply_patch", stableCopilotNativeToolName("workspace", "apply_patch"))
}

func TestCopilotNativeResponsesRestoresCustomCallsAndUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			server, st, _, plain := newTestServer(t)
			st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			provider := &fakeCopilotProvider{
				supportedEndpoints: map[string]map[string]bool{"grok-test": {copilot.EndpointResponses: true}},
				response:           sseResponse(http.StatusOK, nativeCopilotToolResponseFixture()),
			}
			request := map[string]any{"model": "grok-test", "input": "hello", "stream": stream,
				"tools": json.RawMessage(nativeCopilotTestTools)}
			encoded, _ := json.Marshal(request)
			recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", string(encoded))
			body := recorder.Body.String()
			if recorder.Code != http.StatusOK || strings.Contains(body, `"name":"workspace__apply_patch"`) ||
				!strings.Contains(body, `"type":"custom_tool_call"`) || !strings.Contains(body, `"namespace":"workspace"`) ||
				!strings.Contains(body, `"call_id":"call-1"`) || !strings.Contains(body, `"input":"line one\nline two"`) ||
				!strings.Contains(body, `"encrypted_content":"illustrative-opaque-data"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, body)
			}
			if stream && (strings.Contains(body, "response.function_call_arguments.") ||
				strings.Count(body, "event: response.custom_tool_call_input.done") != 1) {
				t.Fatalf("incorrect custom argument stream: %s", body)
			}
			if st.usageInput != 7 || st.usageOutput != 3 || !st.sessionSaved {
				t.Fatalf("usage=%d/%d session=%v", st.usageInput, st.usageOutput, st.sessionSaved)
			}
			for key := range recorder.Header() {
				if strings.HasPrefix(key, "X-Subpool-Internal-") {
					t.Fatalf("internal mapping leaked: %q", key)
				}
			}
		})
	}
}

func TestCopilotNativeResponsesRestoresJSONAndOrdinaryNamespaces(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"id":"resp-json","status":"completed","example":9007199254740993,"output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"files__read","arguments":"{}"}]}`))}
	restoreCopilotNativeResponses(response, map[string]copilotToolInfo{"files__read": {Name: "read", Namespace: "files"}})
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Contains(raw, []byte(`"namespace":"files"`)) || !bytes.Contains(raw, []byte(`"name":"read"`)) ||
		!bytes.Contains(raw, []byte(`"example":9007199254740993`)) {
		t.Fatalf("response=%s error=%v", raw, err)
	}
}

func TestCopilotNativeResponsesCustomStreamWithoutArgumentsDone(t *testing.T) {
	fixture := nativeCopilotToolResponseFixture()
	start := strings.Index(fixture, "event: response.function_call_arguments.done")
	end := strings.Index(fixture[start:], "event: response.output_item.done") + start
	fixture = fixture[:start] + fixture[end:]
	state := &copilotNativeToolStream{mappings: map[string]copilotToolInfo{
		stableCopilotNativeToolName("workspace", "apply_patch"): {Name: "apply_patch", Namespace: "workspace", Custom: true},
	}, calls: make(map[string]*copilotNativeCall)}
	var output bytes.Buffer
	if err := state.writeStream(&output, strings.NewReader(fixture)); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "event: response.custom_tool_call_input.done") != 1 ||
		!strings.Contains(output.String(), `"input":"line one\nline two"`) {
		t.Fatalf("missing custom call completion: %s", output.String())
	}
}

func TestCopilotNativeResponsesRejectsMalformedToolInput(t *testing.T) {
	for _, request := range []string{
		`{"tools":"invalid"}`,
		`{"tools":[null]}`,
		`{"tools":[{"type":"namespace","name":"files","tools":[{"type":"web_search"}]}]}`,
		`{"input":[{"type":"custom_tool_call","name":"patch","input":42}]}`,
		`{"input":[{"type":"additional_tools","role":"user","tools":[]}]}`,
		`{"tools":[{"type":"tool_search","execution":"client"}]}`,
	} {
		if _, _, err := normalizeCopilotNativeResponses([]byte(request), "grok-test", true); err == nil {
			t.Fatalf("accepted malformed tools: %s", request)
		}
	}
}

func TestCopilotNativeResponsesPreservesQuotaFailuresWithMappedTools(t *testing.T) {
	for _, stream := range []bool{false, true} {
		server, st, _, plain := newTestServer(t)
		st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
		st.route.Pool.Provider = domain.ProviderCopilot
		provider := &fakeCopilotProvider{
			supportedEndpoints: map[string]map[string]bool{"grok-test": {copilot.EndpointResponses: true}},
			response: sseResponse(http.StatusOK,
				`data: {"type":"response.failed","response":{"status":"failed","error":{"message":"Illustrative quota exhausted","code":"copilot_quota_exhausted"}}}`+"\n\n"),
		}
		request, _ := json.Marshal(map[string]any{"model": "grok-test", "input": "hello", "stream": stream,
			"tools": json.RawMessage(nativeCopilotTestTools)})
		recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", string(request))
		wantStatus := http.StatusTooManyRequests
		if stream {
			wantStatus = http.StatusOK
		}
		if recorder.Code != wantStatus || !strings.Contains(recorder.Body.String(), "copilot_quota_exhausted") ||
			len(st.availabilityUpdates) != 1 || st.availabilityUpdates[0].allowed {
			t.Fatalf("status=%d updates=%#v body=%s", recorder.Code, st.availabilityUpdates, recorder.Body.String())
		}
	}
}

func TestCopilotNativeResponsesCloseUnblocksUpstream(t *testing.T) {
	upstream, writer := io.Pipe()
	defer writer.Close()
	response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: upstream}
	restoreCopilotNativeResponses(response, map[string]copilotToolInfo{"files__read": {Name: "read", Namespace: "files"}})
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := writer.Write([]byte("data: {}\n\n"))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("upstream was not closed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("closing the adapted response left the upstream blocked")
	}
}

func TestCopilotNativeResponsesRejectsMalformedStreamEvents(t *testing.T) {
	for _, event := range []string{"null", "[]", "invalid"} {
		state := &copilotNativeToolStream{mappings: map[string]copilotToolInfo{}, calls: map[string]*copilotNativeCall{}}
		if err := state.writeStream(io.Discard, strings.NewReader("data: "+event+"\n\n")); err == nil {
			t.Fatalf("malformed event was accepted: %q", event)
		}
	}
}

func TestCopilotNativeResponsesRetainsCustomFormats(t *testing.T) {
	for _, namespace := range []bool{false, true} {
		for _, syntax := range []string{"regex", "lark"} {
			t.Run(fmt.Sprintf("namespace=%t/syntax=%s", namespace, syntax), func(t *testing.T) {
				definition := "^TEST_[0-9]+$"
				if syntax == "lark" {
					definition = "start: \"TEST_\" INT\n%import common.INT"
				}
				tool := map[string]any{"type": "custom", "name": "emit_token", "description": "Return an illustrative token.",
					"format": map[string]any{"type": "grammar", "syntax": syntax, "definition": definition}}
				if namespace {
					tool = map[string]any{"type": "namespace", "name": "diagnostic", "tools": []any{tool}}
				}
				request, _ := json.Marshal(map[string]any{"input": "Return a token.", "tools": []any{tool}})
				body, mappings, err := normalizeCopilotNativeResponses(request, "grok-test", true)
				if err != nil {
					t.Fatal(err)
				}
				var normalized map[string]any
				_ = json.Unmarshal(body, &normalized)
				function := normalized["tools"].([]any)[0].(map[string]any)
				parameters := function["parameters"].(map[string]any)
				input := parameters["properties"].(map[string]any)["input"].(map[string]any)
				if !strings.Contains(input["description"].(string), definition) {
					t.Fatalf("format guidance was lost: %s", body)
				}
				if syntax == "regex" {
					info := mappings[function["name"].(string)]
					if input["pattern"] != definition || info.InputPattern == nil || validateCopilotCustomInput(info, "TEST_7") != nil ||
						validateCopilotCustomInput(info, "invalid") == nil {
						t.Fatalf("regex validation was lost: %s", body)
					}
				}
			})
		}
	}
}

func TestCopilotNativeResponsesRejectsUnsupportedCustomFormats(t *testing.T) {
	for _, format := range []string{
		`"invalid"`, `{"type":"unknown"}`, `{"type":"grammar","syntax":"unknown","definition":"test"}`,
		`{"type":"grammar","syntax":"regex","definition":"["}`, `{"type":"grammar","syntax":"lark"}`,
	} {
		request := `{"input":"hello","tools":[{"type":"custom","name":"emit_token","format":` + format + `}]}`
		if _, _, err := normalizeCopilotNativeResponses([]byte(request), "grok-test", true); err == nil {
			t.Fatalf("accepted unsupported custom format: %s", format)
		}
	}
}

func TestCopilotNativeResponsesRejectsInvalidRegexToolCalls(t *testing.T) {
	request := []byte(`{"input":"hello","tools":[{"type":"custom","name":"emit_token","format":{"type":"grammar","syntax":"regex","definition":"a|ab"}}]}`)
	_, mappings, err := normalizeCopilotNativeResponses(request, "grok-test", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, stream := range []bool{false, true} {
		for _, input := range []string{"ab", "invalid"} {
			t.Run(fmt.Sprintf("stream=%t/input=%s", stream, input), func(t *testing.T) {
				arguments, _ := json.Marshal(map[string]any{"input": input})
				item := map[string]any{"type": "function_call", "id": "fc-test", "call_id": "call-test",
					"name": "emit_token", "status": "completed", "arguments": string(arguments)}
				contentType := "application/json"
				body, _ := json.Marshal(map[string]any{"status": "completed", "output": []any{item}})
				if stream {
					contentType = "text/event-stream"
					body, _ = json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
					body = []byte("data: " + string(body) + "\n\n")
				}
				response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}},
					Body: io.NopCloser(bytes.NewReader(body))}
				restoreCopilotNativeResponses(response, mappings)
				defer response.Body.Close()
				restored, err := io.ReadAll(response.Body)
				if input == "invalid" {
					if err == nil || !strings.Contains(err.Error(), "regex grammar") {
						t.Fatalf("invalid tool call was forwarded: %s error=%v", restored, err)
					}
				} else if err != nil || !bytes.Contains(restored, []byte(`"input":"ab"`)) {
					t.Fatalf("valid alternative was rejected: %s error=%v", restored, err)
				}
			})
		}
	}
}

func TestCopilotNativeResponsesStableContinuationAliases(t *testing.T) {
	tools := []any{map[string]any{"type": "namespace", "name": "workspace", "tools": []any{
		map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}},
	}}}
	first, _ := json.Marshal(map[string]any{"input": "Look up an illustrative record.", "tools": tools})
	_, before, err := normalizeCopilotNativeResponses(first, "grok-test", true)
	if err != nil {
		t.Fatal(err)
	}
	alias := copilotToolName("lookup", "workspace", before)
	tools = append(tools, map[string]any{"type": "function", "name": "workspace__lookup"})
	second, _ := json.Marshal(map[string]any{"previous_response_id": "resp-test", "input": "Continue.", "tools": tools})
	_, after, err := normalizeCopilotNativeResponses(second, "grok-test", true)
	if err != nil || alias != copilotToolName("lookup", "workspace", after) {
		t.Fatalf("native continuation alias changed: before=%#v after=%#v error=%v", before, after, err)
	}
	state := &copilotNativeToolStream{mappings: after, calls: make(map[string]*copilotNativeCall)}
	item := map[string]any{"type": "function_call", "name": alias, "arguments": "{}", "id": "fc-test"}
	if err := state.restoreItem(item); err != nil || item["name"] != "lookup" || item["namespace"] != "workspace" {
		t.Fatalf("previous alias was not restored: %#v error=%v", item, err)
	}
	continuation := []byte(`{"previous_response_id":"resp-test","input":[{"type":"function_call","name":"lookup","namespace":"workspace","arguments":"{}"}]}`)
	withoutTools, _, err := normalizeCopilotNativeResponses(continuation, "grok-test", true)
	if err != nil || !bytes.Contains(withoutTools, []byte(alias)) {
		t.Fatalf("history without declarations changed alias: %s error=%v", withoutTools, err)
	}
}

func TestCopilotNativeResponsesRejectsAliasCollisions(t *testing.T) {
	alias := stableCopilotNativeToolName("workspace", "lookup")
	request := `{"input":"hello","tools":[{"type":"namespace","name":"workspace","tools":[{"type":"function","name":"lookup"}]},{"type":"function","name":"` + alias + `"}]}`
	if _, _, err := normalizeCopilotNativeResponses([]byte(request), "grok-test", true); err == nil {
		t.Fatal("stable alias collision was silently remapped")
	}
	if stableCopilotNativeToolName("a__b", "c") == stableCopilotNativeToolName("a", "b__c") {
		t.Fatal("different tool identities share an alias")
	}
	long := stableCopilotNativeToolName(strings.Repeat("workspace", 30), strings.Repeat("lookup", 30))
	if len(long) > maxCopilotToolNameLength {
		t.Fatalf("tool name exceeds upstream limit: %d", len(long))
	}
}

func TestCopilotNativeResponsesPreservesIncompleteCustomInput(t *testing.T) {
	request := []byte(`{"input":"hello","tools":[{"type":"custom","name":"emit_token","format":{"type":"grammar","syntax":"regex","definition":"TEST_[0-9]+"}}]}`)
	_, mappings, err := normalizeCopilotNativeResponses(request, "grok-test", true)
	if err != nil {
		t.Fatal(err)
	}
	state := &copilotNativeToolStream{mappings: mappings, calls: make(map[string]*copilotNativeCall)}
	item := map[string]any{"type": "function_call", "name": "emit_token", "id": "fc-test", "status": "incomplete", "arguments": `{"input":"TEST_"}`}
	if err := state.restoreItem(item); err != nil || item["type"] != "custom_tool_call" || item["input"] != "TEST_" {
		t.Fatalf("incomplete input was rejected: %#v error=%v", item, err)
	}
}

func nativeCopilotPlainHistory() []byte {
	const item = `{"type":"message","role":"user","content":[{"type":"input_text","text":"illustrative"}]}`
	return []byte(`{"model":"grok-test","input":[` + strings.Repeat(item+",", 39999) + item + `]}`)
}

func TestCopilotNativeResponsesDoesNotDecodePlainHistory(t *testing.T) {
	request := nativeCopilotPlainHistory()
	allocations := testing.AllocsPerRun(1, func() {
		if _, _, err := normalizeCopilotNativeResponses(request, "grok-test", true); err != nil {
			t.Fatal(err)
		}
	})
	if allocations > 128 {
		t.Fatalf("plain history allocations scale with message count: %.0f", allocations)
	}
	const rawItem = `{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,illustrative"}],"number":9007199254740993}`
	body, _, err := normalizeCopilotNativeResponses([]byte(`{"input":[`+rawItem+`]}`), "grok-test", true)
	if err != nil || !bytes.Contains(body, []byte(rawItem)) {
		t.Fatalf("untouched input was re-encoded: %s error=%v", body, err)
	}
}

func BenchmarkCopilotNativePlainHistory(b *testing.B) {
	request := nativeCopilotPlainHistory()
	b.SetBytes(int64(len(request)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, err := normalizeCopilotNativeResponses(request, "grok-test", true); err != nil {
			b.Fatal(err)
		}
	}
}
