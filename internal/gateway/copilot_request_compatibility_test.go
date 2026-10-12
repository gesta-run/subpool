package gateway

import (
	"bytes"
	"encoding/json"
	"github.com/gesta-run/subpool/internal/adapter/protocol"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/jsonobject"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

func TestCopilotAssistantHistoryIsModelScoped(t *testing.T) {
	const message = `{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ALPHA_MARKER","annotations":[],"logprobs":[]}],"example":9007199254740993}`
	for _, model := range []string{"claude-sonnet-5.5", "claude-haiku-4.5", "claude-test", "claude-sonnet-5.5-fast", "grok-4.7", "gemini-3.8-flash", "gpt-5.4"} {
		t.Run(model, func(t *testing.T) {
			for _, native := range []bool{false, true} {
				field := "messages"
				if native {
					field = "input"
				}
				raw := []byte(`{"` + field + `":[` + message + `],"example":9007199254740993}`)
				original := bytes.Clone(raw)
				var normalized []byte
				var err error
				if native {
					normalized, _, err = protocol.NormalizeCopilotNativeResponses(raw, model, true)
				} else {
					normalized, err = protocol.NormalizeCopilotChatHistory(raw, model)
				}
				if err != nil || !bytes.Equal(raw, original) {
					t.Fatalf("normalization error=%v or mutated original", err)
				}
				want := message
				if model == "claude-sonnet-5.5" || model == "claude-haiku-4.5" {
					want = strings.Replace(want, `[{"type":"output_text","text":"ALPHA_MARKER","annotations":[],"logprobs":[]}]`, `"ALPHA_MARKER"`, 1)
				}
				got, _ := jsonobject.Lookup(normalized, field)
				if string(got) != "["+want+"]" || !bytes.Contains(normalized, []byte(`"example":9007199254740993`)) {
					t.Fatalf("unexpected normalized history: %s", normalized)
				}
			}
		})
	}
}

func TestCopilotAssistantHistoryPreservesNonTextAndOtherRoles(t *testing.T) {
	for _, item := range []string{
		`{"role":"user","content":[{"type":"text","text":"USER_DEMO"}]}`,
		`{"role":"system","content":[{"type":"text","text":"SYSTEM_DEMO"}]}`,
		`{"role":"tool","tool_call_id":"call-demo","content":[{"type":"text","text":"TOOL_DEMO"}]}`,
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call-demo","type":"function","function":{"name":"echo","arguments":"{}"}}]}`,
		`{"role":"assistant","content":[]}`,
		`{"role":"assistant","content":"STRING_DEMO"}`,
		`{"role":"assistant","content":[{"type":"input_image","image_url":"data:image/png;base64,illustrative"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"DEMO"},{"type":"refusal","refusal":"Cannot assist"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"DEMO","annotations":[{"type":"illustrative"}]}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"DEMO","logprobs":[{"token":"illustrative"}]}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"DEMO","unknown":"keep"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":null}]}`,
		`{"type":"reasoning","content":null,"encrypted_content":"illustrative"}`,
		`{"type":"future_item","role":"assistant","content":[{"type":"text","text":"KEEP_DEMO"}]}`,
	} {
		t.Run(item, func(t *testing.T) {
			raw := []byte(`{"messages":[` + item + `]}`)
			got, err := protocol.NormalizeCopilotChatHistory(raw, "claude-haiku-4.5")
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatalf("untouched history changed: %s error=%v", got, err)
			}
			got, _, err = protocol.NormalizeCopilotNativeResponses([]byte(`{"input":[`+item+`]}`), "claude-sonnet-5.5", true)
			input, _ := jsonobject.Lookup(got, "input")
			if err != nil || string(input) != "["+item+"]" {
				t.Fatalf("native history changed: %s error=%v", got, err)
			}
		})
	}
}

func TestCopilotAssistantHistoryPreservesTextOrderAndToolCalls(t *testing.T) {
	const message = `{"type":"message","r\u006fle":"assistant","content":[{"type":"text","text":"FIRST\n"},{"type":"output_text","text":"SECOND \"DEMO\""}],"tool_calls":[{"id":"call-demo","type":"function","function":{"name":"echo","arguments":"{}"}}]}`
	got, err := protocol.NormalizeCopilotChatHistory([]byte(`{"messages":[`+message+`]}`), "claude-sonnet-5.5")
	if err != nil || !bytes.Contains(got, []byte(`"content":"FIRST\nSECOND \"DEMO\""`)) ||
		!bytes.Contains(got, []byte(`"tool_calls":[{"id":"call-demo","type":"function","function":{"name":"echo","arguments":"{}"}}]`)) {
		t.Fatalf("text order or tool calls changed: %s error=%v", got, err)
	}
}

func TestCopilotAssistantHistoryPreservesJSONTextSemantics(t *testing.T) {
	for _, parts := range []string{
		`[{"type":"text","text":"quoted \" text\\\n\t\/"},{"type":"text","text":"\u4e2d文<&>\u2028"}]`,
		`[{"type":"text","text":"\uD83D\uDE00"},{"type":"output_text","text":"😀"}]`,
		`[{"type":"text","text":"\ud83d"},{"type":"text","text":"\ude00"}]`,
		`[{"type":"text","text":"\\ud83d"},{"type":"text","text":"\ude00\ud800x"}]`,
		`[{"type":"text","text":""},{"type":"text","text":"\u0000"}]`,
		`[{"type":"text","text":"first","te\u0078t":"last","annotations":null,"logprobs":[ ]}]`,
		`[{"type":"text","text":"` + string([]byte{0xc3}) + `"},{"type":"text","text":"` + string([]byte{0xa9}) + `"}]`,
	} {
		var decoded []struct{ Text string }
		if err := json.Unmarshal([]byte(parts), &decoded); err != nil {
			t.Fatal(err)
		}
		want := ""
		for _, part := range decoded {
			want += part.Text
		}
		for _, native := range []bool{false, true} {
			field := "messages"
			if native {
				field = "input"
			}
			raw := []byte(`{"` + field + `":[{"role":"assistant","content":` + parts + `}]}`)
			var body []byte
			var err error
			if native {
				body, _, err = protocol.NormalizeCopilotNativeResponses(raw, "claude-sonnet-5.5", true)
			} else {
				body, err = protocol.NormalizeCopilotChatHistory(raw, "claude-haiku-4.5")
			}
			messages, _ := jsonobject.Lookup(body, field)
			var got []struct{ Content string }
			if err != nil || json.Unmarshal(messages, &got) != nil || len(got) != 1 || got[0].Content != want {
				t.Fatalf("JSON text changed: input=%q output=%q want=%q error=%v", raw, body, want, err)
			}
		}
	}
}

func TestCopilotAssistantHistoryLargeTextUsesOneOutputCopy(t *testing.T) {
	text := strings.Repeat("x", 8<<20)
	for _, model := range []string{"claude-sonnet-5.5", "claude-haiku-4.5"} {
		for _, native := range []bool{false, true} {
			field := "messages"
			if native {
				field = "input"
			}
			raw := []byte(`{"` + field + `":[{"role":"assistant","content":[{"type":"output_text","text":"` + text + `"},{"type":"text","text":"TAIL_MARKER"}]}]}`)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			var body []byte
			var err error
			if native {
				body, _, err = protocol.NormalizeCopilotNativeResponses(raw, model, true)
			} else {
				body, err = protocol.NormalizeCopilotChatHistory(raw, model)
			}
			runtime.ReadMemStats(&after)
			allocated := after.TotalAlloc - before.TotalAlloc
			// The request is already buffered; only the final output may scale
			// with its size. Allow fixed parser overhead and allocator rounding.
			if allocated > uint64(len(raw))+(256<<10) {
				t.Fatalf("model=%s native=%t input_bytes=%d allocated_bytes=%d", model, native, len(raw), allocated)
			}
			t.Logf("model=%s native=%t input_bytes=%d allocated_bytes=%d", model, native, len(raw), allocated)
			if err != nil || !json.Valid(body) || !bytes.Contains(body, []byte(`TAIL_MARKER"`)) ||
				bytes.Contains(body, []byte(`"content":[`)) || !bytes.Contains(raw, []byte(`"content":[`)) {
				t.Fatalf("large history changed incorrectly: error=%v", err)
			}
		}
	}
}

func TestCopilotRejectsUnsupportedOptionsBeforeUpstream(t *testing.T) {
	for _, test := range []struct{ model, kind, option, code string }{
		{"claude-sonnet-5.5", "responses", `"tool_choice":{"type":"function","name":"echo"}`, "unsupported_tool_choice"},
		{"claude-sonnet-5.5", "responses", `"tool_choice":{"type":"custom","name":"echo","namespace":"diagnostic"}`, "unsupported_tool_choice"},
		{"claude-sonnet-5.5", "responses", `"tool_choice":"required"`, "unsupported_tool_choice"},
		{"claude-sonnet-5.5", "chat/completions", `"tool_choice":{"type":"function","function":{"name":"echo"}}`, "unsupported_tool_choice"},
		{"gemini-3.8-flash", "responses", `"reasoning":{"effort":"xhigh"}`, "invalid_reasoning_effort"},
		{"gemini-3.8-flash", "chat/completions", `"reasoning_effort":"xhigh"`, "invalid_reasoning_effort"},
	} {
		t.Run(test.model+"/"+test.kind+"/"+test.option, func(t *testing.T) {
			server, st, _, plain := newTestServer(t)
			st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			provider := &fakeCopilotProvider{}
			input := `"input":"ILLUSTRATIVE_PROMPT"`
			if test.kind == "chat/completions" {
				input = `"messages":[{"role":"user","content":"ILLUSTRATIVE_PROMPT"}]`
			}
			request := `{"model":"` + test.model + `",` + input + `,` + test.option + `}`
			recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/"+test.kind, request)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"`+test.code+`"`) ||
				provider.chatCalls != 0 || provider.responsesCalls != 0 || len(st.healthFailureCodes) != 0 {
				t.Fatalf("status=%d calls=%d/%d health=%v body=%s", recorder.Code, provider.chatCalls, provider.responsesCalls, st.healthFailureCodes, recorder.Body.String())
			}
		})
	}
}

func TestCopilotSupportedAndOtherModelOptionsRemainUnchanged(t *testing.T) {
	for _, test := range []struct{ model, kind, option string }{
		{"claude-sonnet-5.5", "responses", `"tool_choice":"auto"`},
		{"claude-sonnet-5.5", "chat_completions", `"tool_choice":"none"`},
		{"claude-sonnet-5.5", "responses", `"tool_choice":{"type":"tool_search"}`},
		{"claude-sonnet-5.5", "responses", `"tool_choice":{"type":"allowed_tools","mode":"auto","tools":[]}`},
		{"claude-haiku-4.5", "responses", `"tool_choice":"required"`},
		{"claude-test", "responses", `"tool_choice":{"type":"function","name":"echo"}`},
		{"grok-4.7", "responses", `"reasoning":{"effort":"xhigh"}`},
		{"gemini-3.8-flash", "responses", `"reasoning":{"effort":"high"}`},
		{"gemini-3.8-flash", "chat_completions", `"reasoning_effort":"low"`},
		{"gemini-3.8-flash-fast", "responses", `"reasoning":{"effort":"xhigh"}`},
		{"gemini-3.8-flash", "responses", `"input":"EFFORT_OMITTED"`},
	} {
		raw := []byte(`{` + test.option + `}`)
		original := bytes.Clone(raw)
		if err := protocol.ValidateCopilotRequest(test.kind, test.model, raw); err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("unexpected option rejection or mutation for %s: %v", test.model, err)
		}
	}
}

func TestCopilotUnsupportedOptionsPreserveMixedPoolFallback(t *testing.T) {
	for _, test := range []struct{ model, option string }{
		{"claude-sonnet-5.5", `"tool_choice":"required"`},
		{"gemini-3.8-flash", `"reasoning":{"effort":"xhigh"}`},
	} {
		t.Run(test.model, func(t *testing.T) {
			server, st, _, plain := newTestServer(t)
			cipher := server.cipher.(*credential.Cipher)
			st.route.Account = copilotAccountWithCipher(t, cipher, "copilot-account")
			st.route.Pool.Provider = domain.ProviderMixed
			st.reassigned = compatibleAccountWithCipher(t, cipher, "compatible-account")
			compatible := &fakeCompatibleProvider{}
			server.compatible = compatible
			provider := &fakeCopilotProvider{}
			request := `{"model":"` + test.model + `","stream":true,"input":[{"role":"assistant","content":[{"type":"output_text","text":"OLD_DEMO"}]}],` + test.option + `}`
			recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/responses", request)
			if recorder.Code != http.StatusOK || string(compatible.responsesBody) != request ||
				provider.chatCalls != 0 || provider.responsesCalls != 0 {
				t.Fatalf("status=%d fallback=%s Copilot calls=%d/%d", recorder.Code, compatible.responsesBody, provider.chatCalls, provider.responsesCalls)
			}
		})
	}
}

func TestCopilotChatAndResponsesRoutesNormalizeAssistantHistory(t *testing.T) {
	for _, kind := range []string{"responses", "chat/completions"} {
		for _, native := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/chat", true: "/native"}[native], func(t *testing.T) {
				server, st, _, plain := newTestServer(t)
				st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "copilot-account")
				st.route.Pool.Provider = domain.ProviderCopilot
				endpoint := copilot.EndpointChatCompletions
				if native {
					endpoint = copilot.EndpointResponses
				}
				provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{"claude-haiku-4.5": {endpoint: true}}}
				field := "input"
				if kind == "chat/completions" {
					field = "messages"
				}
				request := `{"model":"claude-haiku-4.5","` + field + `":[{"role":"user","content":"ALPHA_MARKER"},{"role":"assistant","content":[{"type":"text","text":"ALPHA_MARKER"}]},{"role":"user","content":"BRAVO_MARKER"}]}`
				recorder := serveGateway(t, server.WithCopilot(provider), plain, "/v1/"+kind, request)
				var value map[string]any
				if recorder.Code != http.StatusOK || json.Unmarshal(provider.body, &value) != nil {
					t.Fatalf("status=%d upstream=%s response=%s", recorder.Code, provider.body, recorder.Body.String())
				}
				field = "messages"
				if native {
					field = "input"
				}
				messages := value[field].([]any)
				if len(messages) != 3 || messages[1].(map[string]any)["role"] != "assistant" ||
					messages[1].(map[string]any)["content"] != "ALPHA_MARKER" || messages[2].(map[string]any)["content"] != "BRAVO_MARKER" {
					t.Fatalf("message order or assistant content invalid: %s", provider.body)
				}
			})
		}
	}
}
