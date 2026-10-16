package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

func TestCopilotPayloadDiagnosticsMeasureEachProtocolPath(t *testing.T) {
	for _, test := range []struct {
		name, kind, endpoint string
	}{
		{"native responses", "responses", copilot.EndpointResponses},
		{"responses to chat", "responses", copilot.EndpointChatCompletions},
		{"native chat", "chat_completions", copilot.EndpointChatCompletions},
		{"chat to responses", "chat_completions", copilot.EndpointResponses},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			originalLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(originalLogger)
			type wireRequest struct {
				body []byte
				id   string
			}
			wireRequests := make(chan wireRequest, 1)
			var requests atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/token":
					_, _ = fmt.Fprintf(w, `{"token":"illustrative-access-token","expires_at":%d,"refresh_in":3600,"endpoints":{"api":%q}}`, time.Now().Add(time.Hour).Unix(), "http://"+r.Host)
				case "/models":
					_, _ = fmt.Fprintf(w, `{"data":[{"id":"illustrative-model","supported_endpoints":[%q]}]}`, test.endpoint)
				case test.endpoint:
					requests.Add(1)
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					wireRequests <- wireRequest{body: body, id: r.Header.Get("X-Request-Id")}
					w.Header().Set("Content-Type", "text/plain")
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					_, _ = io.WriteString(w, "illustrative-private-upstream-error")
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			client := copilot.NewClient(copilot.ClientConfig{APIBase: upstream.URL, TokenExchangeURL: upstream.URL + "/token", HTTPClient: upstream.Client()})
			provider, err := For(domain.ProviderCopilot, Clients{Copilot: client})
			if err != nil {
				t.Fatal(err)
			}
			request := `{"model":"illustrative-model","client_metadata":{"note":"illustrative-private-metadata"},"input":"illustrative-private-prompt"}`
			if test.kind == "chat_completions" {
				request = `{"model":"illustrative-model","client_metadata":{"note":"illustrative-private-metadata"},"messages":[{"role":"user","content":"illustrative-private-prompt"}]}`
			}
			response, err := provider.RoundTrip(context.Background(), Request{Kind: test.kind, Model: "illustrative-model", Body: []byte(request)},
				http.Header{"Authorization": {"Bearer illustrative-client-key"}}, Credentials{Copilot: copilot.Credentials{GitHubToken: "illustrative-github-token"}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != http.StatusRequestEntityTooLarge || requests.Load() != 1 || !strings.Contains(string(body), `"code":"upstream_payload_too_large"`) {
				t.Fatalf("status=%d requests=%d body=%s", response.StatusCode, requests.Load(), body)
			}
			wire := <-wireRequests
			upstreamBody, upstreamRequestID := wire.body, wire.id
			if upstreamRequestID == "" || response.Header.Get("X-Request-Id") != upstreamRequestID {
				t.Fatal("the outgoing request ID was not retained for diagnostics")
			}
			var record map[string]any
			if err := json.Unmarshal(logs.Bytes(), &record); err != nil {
				t.Fatalf("expected one diagnostic record: %v", err)
			}
			if record["provider"] != domain.ProviderCopilot || record["model"] != "illustrative-model" || record["client_protocol"] != test.kind ||
				record["upstream_endpoint"] != test.endpoint || record["upstream_request_id"] != upstreamRequestID ||
				record["inbound_body_bytes"] != float64(len(request)) || record["upstream_body_bytes"] != float64(len(upstreamBody)) {
				t.Fatalf("incorrect diagnostics: %s", logs.String())
			}
			adaptedBytes, ok := record["adapted_body_bytes"].(float64)
			if !ok || adaptedBytes < float64(len(upstreamBody)) {
				t.Fatalf("incorrect adapted byte count: %s", logs.String())
			}
			if test.kind == "chat_completions" && test.endpoint == copilot.EndpointChatCompletions && adaptedBytes != float64(len(request)) {
				t.Fatal("native chat diagnostics did not measure the pre-sanitization body")
			}
			for _, private := range []string{"illustrative-private-prompt", "illustrative-private-metadata", "illustrative-private-upstream-error", "illustrative-access-token", "illustrative-github-token", "illustrative-client-key"} {
				if strings.Contains(logs.String(), private) || strings.Contains(string(body), private) {
					t.Fatalf("private data appeared in diagnostics: %q", private)
				}
			}
		})
	}
}

type unreadPayloadErrorBody struct {
	closed bool
}

func (*unreadPayloadErrorBody) Read([]byte) (int, error) {
	panic("upstream payload error bodies must not be read")
}

func (body *unreadPayloadErrorBody) Close() error {
	body.closed = true
	return nil
}

func TestCopilotPayloadErrorReplacesBodyAndEncodingHeaders(t *testing.T) {
	original := &unreadPayloadErrorBody{}
	response := &http.Response{
		StatusCode: http.StatusRequestEntityTooLarge, Body: original,
		Header: http.Header{
			"Content-Type": {"text/plain"}, "Content-Encoding": {"gzip"}, "Content-Length": {"999"},
			"Transfer-Encoding": {"chunked"}, "Trailer": {"X-Illustrative-Trailer"}, "X-Github-Request-Id": {"illustrative-upstream-id"},
		},
		TransferEncoding: []string{"chunked"}, Trailer: http.Header{"X-Illustrative-Trailer": {"value"}},
	}
	normalizeCopilotPayloadError(response, Request{Kind: "responses", Model: "illustrative-model"}, copilot.EndpointResponses, 123, 145)
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if !original.closed || response.ContentLength != int64(len(body)) || len(response.TransferEncoding) != 0 || len(response.Trailer) != 0 {
		t.Fatal("upstream body or transport metadata was not replaced correctly")
	}
	for _, name := range []string{"Content-Encoding", "Content-Length", "Transfer-Encoding", "Trailer"} {
		if response.Header.Get(name) != "" {
			t.Fatalf("stale header %s", name)
		}
	}
	if response.Header.Get("Content-Type") != "application/json" || response.Header.Get("X-Subpool-Error-Source") != "upstream" ||
		response.Header.Get("X-Request-Id") != "illustrative-upstream-id" || response.Header.Get("X-Github-Request-Id") != "illustrative-upstream-id" {
		t.Fatalf("incorrect error headers: %#v", response.Header)
	}
}

func TestCopilotPayloadErrorLeavesOtherResponsesUntouched(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway} {
		body := &unreadPayloadErrorBody{}
		response := &http.Response{StatusCode: status, Body: body, Header: http.Header{"Content-Type": {"text/plain"}}}
		normalizeCopilotPayloadError(response, Request{}, copilot.EndpointResponses, 1, 2)
		if response.Body != body || body.closed || response.Header.Get("X-Subpool-Error-Source") != "" {
			t.Fatalf("unrelated status %d was changed", status)
		}
	}
	normalizeCopilotPayloadError(nil, Request{}, copilot.EndpointResponses, 1, 2)
}

func TestCopilotPayloadErrorSanitizesIdentifiersAndRepresentationHeaders(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusRequestEntityTooLarge, Body: &unreadPayloadErrorBody{},
		Header: http.Header{
			"X-Request-Id": {"illustrative arbitrary private text"}, "X-Github-Request-Id": {"illustrative-safe-id"},
			"Etag": {"illustrative-old-body"}, "Content-Md5": {"illustrative-old-checksum"},
			"Digest": {"illustrative-old-digest"}, "Content-Range": {"bytes 0-999/1000"},
			"Last-Modified": {"Mon, 12 Oct 2026 00:00:00 GMT"},
		},
	}
	normalizeCopilotPayloadError(response, Request{}, copilot.EndpointResponses, 1, 2)
	defer response.Body.Close()
	if response.Header.Get("X-Request-Id") != "illustrative-safe-id" {
		t.Fatal("an invalid correlation header was forwarded instead of the sanitized fallback")
	}
	for _, name := range []string{"ETag", "Last-Modified", "Content-MD5", "Digest", "Content-Range"} {
		if response.Header.Get(name) != "" {
			t.Fatalf("rewritten response retained stale representation header %s", name)
		}
	}
}

func TestCopilotDiagnosticTokensRejectArbitraryText(t *testing.T) {
	for _, value := range []string{"Bearer illustrative-secret", "identifier\nprivate-text", strings.Repeat("x", 257)} {
		if copilotDiagnosticToken(value) != "" {
			t.Fatal("arbitrary text was accepted as a diagnostic identifier")
		}
	}
	if got := copilotDiagnosticToken("illustrative-id:0123_abcd.efgh"); got != "illustrative-id:0123_abcd.efgh" {
		t.Fatal("a bounded identifier was rejected")
	}
}

func TestCopilotPayloadClassificationDoesNotGuessFromStatusOrMessages(t *testing.T) {
	for _, test := range []struct {
		status, want int
		code         string
		wantCode     string
	}{
		{413, 400, "context_length_exceeded", "context_length_exceeded"},
		{400, 400, "context_window_exceeded", "context_length_exceeded"},
		{413, 413, "request_body_too_large", "upstream_payload_too_large"},
		{400, 400, "request_body_too_large", "upstream_payload_too_large"},
		{413, 413, "image_too_large", "upstream_payload_too_large"},
		{413, 413, "unknown", "upstream_payload_too_large"},
	} {
		var logs bytes.Buffer
		original := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
		response := &http.Response{StatusCode: test.status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"error":{"code":%q,"message":"illustrative private text; context_length_exceeded"}}`, test.code)))}
		normalizeCopilotPayloadError(response, Request{Kind: "responses", Model: "fixture"}, copilot.EndpointResponses, 10, 10)
		slog.SetDefault(original)
		raw, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != test.want || !strings.Contains(string(raw), `"code":"`+test.wantCode+`"`) {
			t.Fatalf("status=%d body=%s", response.StatusCode, raw)
		}
		if strings.Contains(string(raw), "illustrative private") || strings.Contains(logs.String(), "illustrative private") {
			t.Fatal("classification exposed upstream error text")
		}
	}
}
