package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"io"
	"net/http"
	"reflect"
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

const nativeCopilotUnionSchema = `{
  "type":"object","properties":{},"oneOf":[{"$ref":"#/$defs/manage"},{"$ref":"#/$defs/remove"}],
  "$defs":{
    "manage":{"oneOf":[
      {"type":"object","properties":{"mode":{"enum":["view"]},"id":{"type":"string"}},"required":["mode","id"],"additionalProperties":false},
      {"type":"object","properties":{"mode":{"enum":["create"]}},"required":["mode"],"additionalProperties":false}
    ]},
    "remove":{"type":"object","properties":{"mode":{"enum":["delete"]},"id":{"type":"string"}},"required":["mode","id"],"additionalProperties":false}
  }
}`

func TestCopilotNativeResponsesToolSchemaCompatibility(t *testing.T) {
	for _, model := range []string{"grok-4.7", "grok-4.6", "gpt-6-luna", "claude-sonnet-5.5", "gemini-3.8-flash"} {
		for _, additional := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/additional=%t", model, additional), func(t *testing.T) {
				server, st, _, key := newTestServer(t)
				st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
				st.route.Pool.Provider = domain.ProviderCopilot
				provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{model: {copilot.EndpointResponses: true}}}
				tools := []any{map[string]any{"type": "namespace", "name": "office", "tools": []any{
					map[string]any{"type": "function", "name": "schedule_update", "strict": false, "parameters": json.RawMessage(nativeCopilotUnionSchema)},
				}}}
				message := map[string]any{"type": "message", "id": "msg-illustrative", "role": "user", "content": "Hi"}
				request := map[string]any{"model": model, "stream": true, "input": []any{message}, "tools": tools,
					"reasoning": map[string]any{"effort": "low", "summary": "detailed"}, "include": []string{"reasoning.encrypted_content"},
					"store": false, "parallel_tool_calls": true, "prompt_cache_key": "illustrative-cache-key"}
				if additional {
					delete(request, "tools")
					request["input"] = []any{map[string]any{"type": "additional_tools", "role": "developer", "tools": tools}, message}
				}
				encoded, _ := json.Marshal(request)
				recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses", string(encoded))
				if recorder.Code != http.StatusOK || provider.responsesCalls != 1 || provider.chatCalls != 0 {
					t.Fatalf("status=%d native/chat=%d/%d body=%s", recorder.Code, provider.responsesCalls, provider.chatCalls, recorder.Body.String())
				}
				var upstream map[string]any
				if err := protocol.DecodeCopilotNativeJSON(provider.body, &upstream); err != nil {
					t.Fatal(err)
				}
				tool := upstream["tools"].([]any)[0].(map[string]any)
				parameters := tool["parameters"].(map[string]any)
				var original map[string]any
				_ = protocol.DecodeCopilotNativeJSON([]byte(nativeCopilotUnionSchema), &original)
				if strings.HasPrefix(model, "grok-") {
					if len(parameters["oneOf"].([]any)) != 3 || !reflect.DeepEqual(original["$defs"], parameters["$defs"]) {
						t.Fatalf("schema was not safely flattened: %#v", parameters)
					}
				} else if !reflect.DeepEqual(original, parameters) {
					t.Fatalf("other model schema was changed: %#v", parameters)
				}
				if tool["name"] != protocol.StableCopilotNativeToolName("office", "schedule_update") || tool["strict"] != false {
					t.Fatalf("tool identity or strict mode changed: %#v", tool)
				}
				for _, field := range []string{"reasoning", "include", "store", "parallel_tool_calls", "prompt_cache_key"} {
					want, _ := json.Marshal(request[field])
					got, _ := json.Marshal(upstream[field])
					if !bytes.Equal(want, got) {
						t.Fatalf("native %s was changed: want=%s got=%s", field, want, got)
					}
				}
				if len(upstream["input"].([]any)) != 1 || !reflect.DeepEqual(upstream["input"].([]any)[0], message) {
					t.Fatalf("message history was changed: %#v", upstream["input"])
				}
			})
		}
	}
}

func TestCopilotUnsafeToolSchemaPreservesFallbackOriginal(t *testing.T) {
	for _, test := range []struct{ name, parameters, message string }{
		{"overlapping union", `{"oneOf":[{"oneOf":[{"type":"object"},{"type":"object"}]}]}`, "overlapping oneOf"},
		{"scoped reference sibling", `{"oneOf":[{"$ref":"#/$defs/base","additionalProperties":false}],"$defs":{"base":{"type":"object","properties":{"id":{"type":"string"}}}}}`, `sibling \"additionalProperties\" validation constraint`},
	} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mixed=%t", test.name, mixed), func(t *testing.T) {
				server, st, _, key := newTestServer(t)
				cipher := server.cipher.(*credential.Cipher)
				st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
				st.route.Pool.Provider = domain.ProviderCopilot
				compatible := &fakeCompatibleProvider{}
				if mixed {
					st.route.Pool.Provider = domain.ProviderMixed
					st.reassigned = compatibleAccountWithCipher(t, cipher, "compatible-account")
					server.compatible = compatible
				}
				provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{"grok-4.7": {copilot.EndpointResponses: true}}}
				request := fmt.Sprintf(`{"model":"grok-4.7","stream":true,"input":"Hi","tools":[{"type":"function","name":"illustrative","parameters":%s}]}`, test.parameters)
				recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses", request)
				if provider.responsesCalls != 0 || provider.chatCalls != 0 {
					t.Fatal("unsafe schema was forwarded to Copilot")
				}
				if mixed {
					if recorder.Code != http.StatusOK || string(compatible.responsesBody) != request {
						t.Fatalf("fallback original changed: status=%d request=%s", recorder.Code, compatible.responsesBody)
					}
				} else if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), test.message) {
					t.Fatalf("unsafe conversion did not return a clear error: status=%d body=%s", recorder.Code, recorder.Body.String())
				}
			})
		}
	}
}

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
	raw, mappings, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-test", true)
	if err != nil {
		t.Fatal(err)
	}
	var upstream map[string]any
	if err := protocol.DecodeCopilotNativeJSON(raw, &upstream); err != nil {
		t.Fatal(err)
	}
	lookup := protocol.CopilotToolName("lookup", "workspace", mappings)
	if lookup == "workspace__lookup" || len(lookup) > protocol.MaxCopilotToolNameLength {
		t.Fatalf("namespace name collision was not handled: %q", lookup)
	}
	items := upstream["input"].([]any)
	call := items[2].(map[string]any)
	if call["name"] != lookup || call["namespace"] != nil || call["id"] != "fc-1" || call["call_id"] != "call-1" {
		t.Fatalf("function history=%#v", call)
	}
	custom := items[4].(map[string]any)
	if custom["type"] != "function_call" || custom["name"] != protocol.StableCopilotNativeToolName("workspace", "apply_patch") || custom["input"] != nil ||
		protocol.CustomToolInput(custom["arguments"].(string)) != "line one\nline two" || custom["call_id"] != "call-2" ||
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

func TestCopilotNativeResponsesCustomToolOutputIDs(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "grok-4.7"} {
		for _, test := range []struct {
			name, id, output string
		}{
			{"desktop text result", `,"id":"ctco_illustrative_result"`, `"illustrative result"`},
			{"desktop rich result", `,"id":"ctco_illustrative_result"`, `[{"type":"input_text","text":"illustrative result"}]`},
			{"result without id", "", `"illustrative result"`},
		} {
			t.Run(model+"/"+test.name, func(t *testing.T) {
				server, st, _, key := newTestServer(t)
				st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
				st.route.Pool.Provider = domain.ProviderCopilot
				provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{model: {copilot.EndpointResponses: true}}}
				request := fmt.Sprintf(`{"model":%q,"tools":%s,"input":[
					{"type":"custom_tool_call","id":"fc_illustrative_call","call_id":"call-custom","name":"apply_patch","namespace":"workspace","input":"illustrative input"},
					{"type":"custom_tool_call_output"%s,"call_id":"call-custom","output":%s},
					{"type":"function_call","id":"fc_illustrative_function","call_id":"call-function","name":"lookup","namespace":"workspace","arguments":"{}"},
					{"type":"function_call_output","id":"fc_illustrative_result","call_id":"call-function","output":"illustrative function result"},
					{"role":"user","content":"Continue"}
				]}`, model, nativeCopilotTestTools, test.id, test.output)
				recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses", request)
				if recorder.Code != http.StatusOK || provider.responsesCalls != 1 || provider.chatCalls != 0 {
					t.Fatalf("status=%d native/chat=%d/%d body=%s", recorder.Code, provider.responsesCalls, provider.chatCalls, recorder.Body.String())
				}
				var upstream, original map[string]any
				if err := protocol.DecodeCopilotNativeJSON(provider.body, &upstream); err != nil {
					t.Fatal(err)
				}
				if err := protocol.DecodeCopilotNativeJSON([]byte(request), &original); err != nil {
					t.Fatal(err)
				}
				items := upstream["input"].([]any)
				originalItems := original["input"].([]any)
				want := originalItems[1].(map[string]any)
				want["type"] = "function_call_output"
				delete(want, "id")
				if !reflect.DeepEqual(items[1], want) {
					t.Fatalf("custom result identity or output changed: want=%#v got=%#v", want, items[1])
				}
				call := items[0].(map[string]any)
				if call["id"] != "fc_illustrative_call" || call["call_id"] != "call-custom" {
					t.Fatalf("custom call identity changed: %#v", call)
				}
				if !reflect.DeepEqual(items[3:], originalItems[3:]) {
					t.Fatalf("native function result or next message changed: %#v", items[3:])
				}
			})
		}
	}
}

func TestCopilotNativeResponsesAdditionalAndDeferredTools(t *testing.T) {
	request := `{"tools":[{"type":"tool_search","execution":"client"}],"input":[
		{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"files","tools":[{"type":"function","name":"read","defer_loading":true,"parameters":{"type":"object"}}]}]},
		{"type":"message","role":"user","content":"read a file"}
	],"tool_choice":{"type":"tool_search"}}`
	raw, mappings, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-test", true)
	if err != nil || len(mappings) != 1 || strings.Contains(string(raw), "tool_search") || strings.Contains(string(raw), "additional_tools") ||
		strings.Contains(string(raw), "defer_loading") || !strings.Contains(string(raw), "files__read") {
		t.Fatalf("request=%s mappings=%#v error=%v", raw, mappings, err)
	}
}

func TestCopilotNativeResponsesToolChoiceFiltering(t *testing.T) {
	lookupChoice := `"name":"` + protocol.StableCopilotNativeToolName("workspace", "lookup") + `"`
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
			raw, _, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-test", true)
			if (err != nil) != test.wantError || err == nil && !strings.Contains(string(raw), test.want) {
				t.Fatalf("request=%s error=%v", raw, err)
			}
		})
	}
	for _, choice := range []string{`"auto"`, `"none"`, `"required"`} {
		request := `{"input":"hi","tools":[{"type":"web_search"}],"tool_choice":` + choice + `}`
		raw, _, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-test", true)
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

	`, "workspace__apply_patch", protocol.StableCopilotNativeToolName("workspace", "apply_patch"))
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
	protocol.RestoreCopilotNativeResponses(response, map[string]protocol.ToolInfo{"files__read": {Name: "read", Namespace: "files"}})
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Contains(raw, []byte(`"namespace":"files"`)) || !bytes.Contains(raw, []byte(`"name":"read"`)) ||
		!bytes.Contains(raw, []byte(`"example":9007199254740993`)) {
		t.Fatalf("response=%s error=%v", raw, err)
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
		if _, _, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-test", true); err == nil {
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
	protocol.RestoreCopilotNativeResponses(response, map[string]protocol.ToolInfo{"files__read": {Name: "read", Namespace: "files"}})
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
				body, mappings, err := protocol.NormalizeCopilotNativeResponses(request, "grok-test", true)
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
					if input["pattern"] != definition || info.InputPattern == nil || protocol.ValidateCopilotCustomInput(info, "TEST_7") != nil ||
						protocol.ValidateCopilotCustomInput(info, "invalid") == nil {
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
		if _, _, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-test", true); err == nil {
			t.Fatalf("accepted unsupported custom format: %s", format)
		}
	}
}

func TestCopilotNativeResponsesRejectsInvalidRegexToolCalls(t *testing.T) {
	request := []byte(`{"input":"hello","tools":[{"type":"custom","name":"emit_token","format":{"type":"grammar","syntax":"regex","definition":"a|ab"}}]}`)
	_, mappings, err := protocol.NormalizeCopilotNativeResponses(request, "grok-test", true)
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
				protocol.RestoreCopilotNativeResponses(response, mappings)
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

func TestCopilotNativeResponsesRejectsAliasCollisions(t *testing.T) {
	alias := protocol.StableCopilotNativeToolName("workspace", "lookup")
	request := `{"input":"hello","tools":[{"type":"namespace","name":"workspace","tools":[{"type":"function","name":"lookup"}]},{"type":"function","name":"` + alias + `"}]}`
	if _, _, err := protocol.NormalizeCopilotNativeResponses([]byte(request), "grok-test", true); err == nil {
		t.Fatal("stable alias collision was silently remapped")
	}
	if protocol.StableCopilotNativeToolName("a__b", "c") == protocol.StableCopilotNativeToolName("a", "b__c") {
		t.Fatal("different tool identities share an alias")
	}
	long := protocol.StableCopilotNativeToolName(strings.Repeat("workspace", 30), strings.Repeat("lookup", 30))
	if len(long) > protocol.MaxCopilotToolNameLength {
		t.Fatalf("tool name exceeds upstream limit: %d", len(long))
	}
}

func nativeCopilotPlainHistory() []byte {
	const item = `{"type":"message","role":"user","content":[{"type":"input_text","text":"illustrative"}]}`
	return []byte(`{"model":"grok-test","input":[` + strings.Repeat(item+",", 39999) + item + `]}`)
}

func TestCopilotNativeResponsesDoesNotDecodePlainHistory(t *testing.T) {
	request := nativeCopilotPlainHistory()
	allocations := testing.AllocsPerRun(1, func() {
		if _, _, err := protocol.NormalizeCopilotNativeResponses(request, "grok-test", true); err != nil {
			t.Fatal(err)
		}
	})
	if allocations > 128 {
		t.Fatalf("plain history allocations scale with message count: %.0f", allocations)
	}
	const rawItem = `{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,illustrative"}],"number":9007199254740993}`
	body, _, err := protocol.NormalizeCopilotNativeResponses([]byte(`{"input":[`+rawItem+`]}`), "grok-test", true)
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
		if _, _, err := protocol.NormalizeCopilotNativeResponses(request, "grok-test", true); err != nil {
			b.Fatal(err)
		}
	}
}

func nativeCopilotChangingIDsFixture() string {
	return `data: {"type":"response.created","response":{"id":"resp-start","output":[]}}

data: {"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"reason-start","summary":[],"status":"in_progress"}}

data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"reason-done","summary":[],"status":"completed","encrypted_content":"illustrative-opaque-data"}}

data: {"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg-start","role":"assistant","status":"in_progress","content":[]}}

data: {"type":"response.content_part.added","output_index":1,"content_index":0,"item_id":"msg-part","part":{"type":"output_text","text":""}}

data: {"type":"response.output_text.delta","output_index":1,"content_index":0,"item_id":"msg-delta","delta":"Hi"}

data: {"type":"response.output_text.done","output_index":1,"content_index":0,"item_id":"msg-text","text":"Hi"}

data: {"type":"response.content_part.done","output_index":1,"content_index":0,"item_id":"msg-content","part":{"type":"output_text","text":"Hi"}}

data: {"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg-done","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hi"}]}}

data: {"type":"response.completed","response":{"id":"resp-final","status":"completed","output":[{"type":"reasoning","id":"reason-final","summary":[],"status":"completed","encrypted_content":"illustrative-opaque-data"},{"type":"message","id":"msg-final","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":7,"output_tokens":3}}}

`
}

func TestCopilotNativeResponsesStableItemIDs(t *testing.T) {
	for _, transport := range []string{"json", "stream", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			server, st, _, key := newTestServer(t)
			st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			provider := &fakeCopilotProvider{
				supportedEndpoints: map[string]map[string]bool{"grok-test": {copilot.EndpointResponses: true}},
				response:           sseResponse(http.StatusOK, nativeCopilotChangingIDsFixture()),
			}
			server.WithCopilot(provider)
			var events []map[string]any
			if transport == "websocket" {
				server.WithResponsesWebSocket(true, false, "")
				client, cleanup := dialResponsesWSTestServer(t, server, key)
				defer cleanup()
				writeResponsesWSMessage(t, client, `{"type":"response.create","model":"grok-test","input":"Hi"}`)
				for {
					event := readResponsesWSMessage(t, client)
					events = append(events, event)
					if event["type"] == "response.completed" {
						break
					}
				}
			} else {
				request := fmt.Sprintf(`{"model":"grok-test","stream":%t,"input":"Hi"}`, transport == "stream")
				recorder := serveGateway(t, server, key, "/v1/responses", request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				if transport == "json" {
					var response map[string]any
					if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					events = []map[string]any{{"type": "response.completed", "response": response}}
				} else {
					events = nativeCopilotParseEvents(t, recorder.Body.String())
				}
			}
			assertNativeCopilotStableIDs(t, events)
		})
	}
}

func nativeCopilotParseEvents(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data:")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	return events
}

func assertNativeCopilotStableIDs(t *testing.T, events []map[string]any) {
	t.Helper()
	ids := []string{"reason-start", "msg-start"}
	var terminal map[string]any
	for _, event := range events {
		if index, ok := event["output_index"].(float64); ok {
			want := ids[int(index)]
			if item, ok := event["item"].(map[string]any); ok && item["id"] != want {
				t.Fatalf("item identity changed: %#v", event)
			}
			if id, ok := event["item_id"].(string); ok && id != want {
				t.Fatalf("delta identity changed: %#v", event)
			}
		}
		if event["type"] == "response.completed" {
			terminal = event["response"].(map[string]any)
		}
	}
	if terminal == nil || terminal["id"] != "resp-final" {
		t.Fatalf("terminal response identity was lost: %#v", terminal)
	}
	output := terminal["output"].([]any)
	if len(output) != 2 {
		t.Fatalf("unexpected output: %#v", output)
	}
	for index, entry := range output {
		if entry.(map[string]any)["id"] != ids[index] {
			t.Fatalf("terminal item identity changed: %#v", entry)
		}
	}
	if output[0].(map[string]any)["encrypted_content"] != "illustrative-opaque-data" {
		t.Fatal("completed reasoning content was lost")
	}
}

func TestCopilotNativeResponsesStreamsBeforeCompletionWithoutTools(t *testing.T) {
	upstream, writer := io.Pipe()
	defer writer.Close()
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: upstream}
	protocol.RestoreCopilotNativeResponses(response, nil)
	defer response.Body.Close()
	go func() {
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"item_id\":\"msg-native\",\"delta\":\"Hi\"}\n\n")
	}()
	result := make(chan string, 1)
	go func() {
		reader := bufio.NewReader(response.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil || strings.HasPrefix(line, "data:") {
				result <- line
				return
			}
		}
	}()
	select {
	case line := <-result:
		if !strings.Contains(line, `"delta":"Hi"`) {
			t.Fatalf("first delta was lost: %s", line)
		}
	case <-time.After(time.Second):
		t.Fatal("stream buffered output until completion")
	}
}
