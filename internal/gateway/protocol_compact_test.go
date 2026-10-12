package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/domain"
)

const illustrativeCompactResponse = `{"id":"cmp_illustrative","object":"response.compaction","output":[{"type":"message","role":"user","content":"Retained fixture"},{"type":"compaction","id":"compact_illustrative","encrypted_content":"illustrative-opaque-data"}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}`

func TestNativeCompactionPreservesCanonicalWindow(t *testing.T) {
	for _, provider := range []string{domain.ProviderCodex, domain.ProviderOpenAICompatible} {
		t.Run(provider, func(t *testing.T) {
			request := `{"model":"illustrative-model","input":[{"role":"user","content":"hello"}]}`
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses/compact" || r.Header.Get("Accept") != "application/json" {
					t.Errorf("path=%s accept=%s", r.URL.Path, r.Header.Get("Accept"))
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != request {
					t.Errorf("compact request was rewritten: %s", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, illustrativeCompactResponse)
			}))
			defer upstream.Close()
			server, st, _, key := newTestServer(t)
			configureMatrixProvider(t, server, st, upstream, provider, true)
			recorder := serveGateway(t, server, key, "/v1/responses/compact", request)
			var want, got any
			_ = json.Unmarshal([]byte(illustrativeCompactResponse), &want)
			_ = json.Unmarshal(recorder.Body.Bytes(), &got)
			if recorder.Code != 200 || !reflect.DeepEqual(want, got) {
				t.Fatalf("canonical window changed: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if st.sessionSaved || st.usageInput != 8 || st.usageOutput != 2 {
				t.Fatalf("session=%v usage=%d/%d", st.sessionSaved, st.usageInput, st.usageOutput)
			}
		})
	}
}

func TestUnsupportedCompactionReturnsCapabilityError(t *testing.T) {
	for _, provider := range []string{domain.ProviderCodex, domain.ProviderOpenAICompatible, domain.ProviderCopilot} {
		t.Run(provider, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses/compact" {
					t.Errorf("compaction must not fall back to %s", r.URL.Path)
				}
				http.NotFound(w, r)
			}))
			defer upstream.Close()
			server, st, _, key := newTestServer(t)
			configureMatrixProvider(t, server, st, upstream, provider, true)
			recorder := serveGateway(t, server, key, "/v1/responses/compact", `{"model":"illustrative-model","input":[]}`)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"unsupported_compaction"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCompactRejectsStreamingContinuationAndMissingScope(t *testing.T) {
	for _, test := range []struct {
		body   string
		scope  string
		status int
	}{
		{`{"model":"illustrative-model","input":[],"stream":true}`, "responses", 400},
		{`{"model":"illustrative-model","input":[],"previous_response_id":"resp_illustrative"}`, "responses", 400},
		{`{"model":"illustrative-model","input":[]}`, "chat_completions", 403},
	} {
		server, st, _, key := newTestServer(t)
		st.route.Key.Scopes = []string{test.scope}
		recorder := serveGateway(t, server, key, "/v1/responses/compact", test.body)
		if recorder.Code != test.status {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
}

func TestInvalidNativeCompactResponseIsNotCounted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"response.compaction","output":null,"usage":{"input_tokens":8,"output_tokens":2}}`)
	}))
	defer upstream.Close()
	server, st, _, key := newTestServer(t)
	configureMatrixProvider(t, server, st, upstream, domain.ProviderCodex, true)
	recorder := serveGateway(t, server, key, "/v1/responses/compact", `{"model":"illustrative-model","input":[]}`)
	if recorder.Code != 502 || st.usageInput != 0 || st.usageOutput != 0 {
		t.Fatalf("status=%d usage=%d/%d body=%s", recorder.Code, st.usageInput, st.usageOutput, recorder.Body.String())
	}
}
