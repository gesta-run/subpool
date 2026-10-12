package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

func TestProtocolCompatibleReasoningTokenLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if request["max_tokens"] != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"unsupported_parameter","message":"Use max_completion_tokens instead of max_tokens"}}`)
			return
		}
		if request["max_completion_tokens"] != float64(1024) {
			t.Errorf("token limit lost: %v", request)
		}
		_, _ = io.WriteString(w, `{"id":"chatcmpl-illustrative","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	server, st, _, key := newTestServer(t)
	configureMatrixProvider(t, server, st, upstream, domain.ProviderOpenAICompatible, false)
	recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"o3","input":"hello","max_output_tokens":1024}`)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "OK") {
		t.Fatalf("valid reasoning request rejected after fallback: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestProtocolCopilotChatKeepsTokenLimit(t *testing.T) {
	server, st, _, key := newTestServer(t)
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-copilot-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := new(fakeCopilotProvider)
	recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/responses", `{"model":"illustrative-model","input":"hello","max_output_tokens":1024}`)
	var request map[string]any
	if err := json.Unmarshal(provider.body, &request); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || provider.chatCalls != 1 || request["max_tokens"] != float64(1024) || request["max_completion_tokens"] != nil {
		t.Fatalf("Copilot token-limit policy changed: status=%d body=%s request=%s", recorder.Code, recorder.Body.String(), provider.body)
	}
}

func TestProtocolCompatibleChatOnlyUpstream(t *testing.T) {
	responsesCalls, chatCalls := 0, 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			responsesCalls++
			http.Error(w, `{"error":{"message":"endpoint not found"}}`, http.StatusNotFound)
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		chatCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_illustrative","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3}}` + "\n\n" + "data: [DONE]\n\n"))
	}))
	defer upstream.Close()
	server, st, _, key := newTestServer(t)
	cipher := server.cipher.(*credential.Cipher)
	account := compatibleAccountWithCipher(t, cipher, "illustrative-compatible-account")
	raw, err := json.Marshal(openaicompat.Credentials{BaseURL: upstream.URL + "/v1", APIKey: "illustrative-key"})
	if err != nil {
		t.Fatal(err)
	}
	account.CredentialCiphertext, err = cipher.Encrypt(raw)
	if err != nil {
		t.Fatal(err)
	}
	st.route.Account = account
	st.route.Pool.Provider = domain.ProviderOpenAICompatible
	server.compatible = openaicompat.NewClient(upstream.Client())
	recorder := serveGateway(t, server, key, "/v1/responses", `{"model":"illustrative-model","stream":true,"input":"hello"}`)
	if recorder.Code != http.StatusOK || chatCalls == 0 {
		t.Fatalf("Codex Responses cannot use a Chat-only compatible provider: status=%d responses/chat calls=%d/%d body=%s", recorder.Code, responsesCalls, chatCalls, recorder.Body.String())
	}
}

func TestProtocolChatIncludeUsageOnResponsesUpstream(t *testing.T) {
	server, st, _, key := newTestServer(t)
	st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-copilot-account")
	st.route.Pool.Provider = domain.ProviderCopilot
	provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{
		"illustrative-model": {copilot.EndpointResponses: true},
	}, response: sseResponse(http.StatusOK,
		`data: {"type":"response.created","response":{"id":"resp_illustrative"}}`+"\n\n"+
			`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"OK"}`+"\n\n"+
			`data: {"type":"response.completed","response":{"id":"resp_illustrative","status":"completed","output":[],"usage":{"input_tokens":7,"output_tokens":3}}}`+"\n\n")}
	recorder := serveGateway(t, server.WithCopilot(provider), key, "/v1/chat/completions", `{"model":"illustrative-model","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}]}`)
	if recorder.Code != http.StatusOK || st.usageInput != 7 || st.usageOutput != 3 {
		t.Fatalf("fixture failed: status=%d usage=%d/%d body=%s", recorder.Code, st.usageInput, st.usageOutput, recorder.Body.String())
	}
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		if usage, ok := chunk["usage"].(map[string]any); ok && usage["total_tokens"] == float64(10) {
			if choices, ok := chunk["choices"].([]any); ok && len(choices) == 0 {
				return
			}
		}
	}
	t.Fatalf("requested usage chunk absent although gateway recorded 7/3 tokens: %s", recorder.Body.String())
}

func TestProtocolCopilotNativeNormalizationIsIngressIndependent(t *testing.T) {
	for _, kind := range []string{"responses", "chat_completions"} {
		t.Run(kind, func(t *testing.T) {
			server, st, _, key := newTestServer(t)
			st.route.Account = copilotAccountWithCipher(t, server.cipher.(*credential.Cipher), "illustrative-copilot-account")
			st.route.Pool.Provider = domain.ProviderCopilot
			provider := &fakeCopilotProvider{supportedEndpoints: map[string]map[string]bool{
				"grok-4.7": {copilot.EndpointResponses: true},
			}}
			parameters := `{"type":"object","properties":{},"oneOf":[{"$ref":"#/$defs/path"}],"$defs":{"path":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}`
			request := `{"model":"grok-4.7","input":"hello","tools":[{"type":"function","name":"lookup","parameters":` + parameters + `}]}`
			path := "/v1/responses"
			if kind == "chat_completions" {
				path = "/v1/chat/completions"
				request = `{"model":"grok-4.7","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":` + parameters + `}}]}`
			}
			if !json.Valid([]byte(request)) {
				t.Fatal("invalid illustrative fixture")
			}
			recorder := serveGateway(t, server.WithCopilot(provider), key, path, request)
			if recorder.Code != http.StatusOK || provider.responsesCalls != 1 {
				t.Fatalf("fixture failed: status=%d responses calls=%d body=%s", recorder.Code, provider.responsesCalls, recorder.Body.String())
			}
			var native map[string]any
			if err := json.Unmarshal(provider.body, &native); err != nil {
				t.Fatal(err)
			}
			tool := native["tools"].([]any)[0].(map[string]any)
			schema := tool["parameters"].(map[string]any)
			branch := schema["oneOf"].([]any)[0].(map[string]any)
			if branch["type"] != "object" || branch["$ref"] != nil {
				t.Fatalf("native Grok schema normalization bypassed for %s ingress: %s", kind, provider.body)
			}
		})
	}
}

func TestProtocolResponsesCompactEndpoint(t *testing.T) {
	server, _, _, key := newTestServer(t)
	recorder := serveGateway(t, server, key, "/v1/responses/compact", `{"model":"illustrative-model","input":[{"role":"user","content":"hello"}]}`)
	if recorder.Code == http.StatusNotFound {
		t.Fatalf("standalone Responses compaction is not registered: status=%d", recorder.Code)
	}
}
