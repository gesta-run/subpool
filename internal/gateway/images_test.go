package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
)

func (f *fakeProvider) ImageGenerations(_ context.Context, body []byte, headers http.Header, credentials codex.Credentials) (*http.Response, error) {
	f.imageGenerationBodies = append(f.imageGenerationBodies, append([]byte(nil), body...))
	return f.imageResponse(headers, credentials)
}

func (f *fakeProvider) ImageEdits(_ context.Context, body []byte, headers http.Header, credentials codex.Credentials) (*http.Response, error) {
	f.imageEditBodies = append(f.imageEditBodies, append([]byte(nil), body...))
	return f.imageResponse(headers, credentials)
}

func (f *fakeProvider) imageResponse(headers http.Header, credentials codex.Credentials) (*http.Response, error) {
	f.credentials = append(f.credentials, credentials)
	f.headers = append(f.headers, headers.Clone())
	if len(f.errors) > 0 {
		err := f.errors[0]
		f.errors = f.errors[1:]
		if err != nil {
			return nil, err
		}
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func TestImageGenerationsUseBoundCodexSubscription(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.route.Key.Scopes = []string{"responses"}
	requestBody := `{"model":"gpt-image-2","prompt":"draw a lighthouse","size":"1024x1024"}`
	imageData := strings.Repeat("a", 1<<20+128)
	responseBody := `{"created":1,"data":[{"b64_json":"` + imageData + `"}],"usage":{"input_tokens":1474,"output_tokens":1372,"total_tokens":2846}}`
	provider.responses = []*http.Response{{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"X-Upstream":   []string{"codex"},
		},
		Body: io.NopCloser(strings.NewReader(responseBody)),
	}}

	recorder := serveGateway(t, server, plain, "/v1/images/generations", requestBody)

	if recorder.Code != http.StatusOK || recorder.Body.String() != responseBody {
		t.Fatalf("response = %d, bytes=%d, want bytes=%d", recorder.Code, recorder.Body.Len(), len(responseBody))
	}
	if recorder.Header().Get("Content-Type") != "application/json" || recorder.Header().Get("X-Upstream") != "codex" {
		t.Fatalf("headers = %#v", recorder.Header())
	}
	if recorder.Header().Get(accountHeader) != "" || recorder.Header().Get(formatHeader) != "" {
		t.Fatalf("internal headers leaked: %#v", recorder.Header())
	}
	if len(provider.imageGenerationBodies) != 1 || string(provider.imageGenerationBodies[0]) != requestBody || len(provider.imageEditBodies) != 0 {
		t.Fatalf("generation/edit requests = %q/%q", provider.imageGenerationBodies, provider.imageEditBodies)
	}
	if len(provider.credentials) != 1 || provider.credentials[0].AccessToken != "old-token" || provider.credentials[0].AccountID != "upstream-account" {
		t.Fatalf("credentials = %#v", provider.credentials)
	}
	if provider.headers[0].Get("X-Codex-Installation-Id") == "" {
		t.Fatalf("missing Codex device identity headers: %#v", provider.headers[0])
	}
	if st.usageInput != 1474 || st.usageOutput != 1372 {
		t.Fatalf("usage = %d/%d", st.usageInput, st.usageOutput)
	}
}

func TestImageEditsForwardJSONDataURLsAndUpstreamErrors(t *testing.T) {
	server, _, provider, plain := newTestServer(t)
	requestBody := `{"model":"gpt-image-2","prompt":"add clouds","images":[{"image_url":"data:image/png;base64,aGVsbG8="}]}`
	responseBody := `{"error":{"message":"invalid source image","type":"invalid_request_error","code":"invalid_image"}}`
	provider.responses = []*http.Response{{
		StatusCode: http.StatusUnprocessableEntity,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}}

	recorder := serveGateway(t, server, plain, "/v1/images/edits", requestBody)

	if recorder.Code != http.StatusUnprocessableEntity || recorder.Body.String() != responseBody {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if len(provider.imageEditBodies) != 1 || string(provider.imageEditBodies[0]) != requestBody || len(provider.imageGenerationBodies) != 0 {
		t.Fatalf("generation/edit requests = %q/%q", provider.imageGenerationBodies, provider.imageEditBodies)
	}
}

func TestImagesRejectPoolsWithoutCodexSubscriptions(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.route.Pool.Provider = domain.ProviderOpenAICompatible
	st.route.Account = domain.ProviderAccount{
		ID: "compatible-account", Provider: domain.ProviderOpenAICompatible,
		CredentialType: domain.CredentialAPIKey, Status: domain.AccountActive,
	}

	recorder := serveGateway(t, server, plain, "/v1/images/generations", `{"model":"gpt-image-2","prompt":"hello"}`)

	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"unsupported_provider_endpoint"`) {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if len(provider.imageGenerationBodies) != 0 || len(provider.imageEditBodies) != 0 {
		t.Fatalf("unexpected Codex image request: %#v %#v", provider.imageGenerationBodies, provider.imageEditBodies)
	}
}

func TestImagesFailOverToCodexSubscriptionInMixedPool(t *testing.T) {
	server, st, provider, plain := newTestServer(t)
	st.route.Pool.Provider = domain.ProviderMixed
	st.route.Account = domain.ProviderAccount{
		ID: "compatible-account", Provider: domain.ProviderOpenAICompatible,
		CredentialType: domain.CredentialAPIKey, Status: domain.AccountActive,
	}
	st.reassigned = accountWithCredentials(t, "codex-account", "image-token")
	provider.responses = []*http.Response{{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"created":1,"data":[]}`)),
	}}

	recorder := serveGateway(t, server, plain, "/v1/images/generations", `{"model":"gpt-image-2","prompt":"hello"}`)

	if recorder.Code != http.StatusOK || len(provider.credentials) != 1 || provider.credentials[0].AccessToken != "image-token" {
		t.Fatalf("response=%d %s credentials=%#v", recorder.Code, recorder.Body.String(), provider.credentials)
	}
	if len(st.reassignExcludes) != 1 || len(st.reassignExcludes[0]) != 1 || st.reassignExcludes[0][0] != "compatible-account" {
		t.Fatalf("reassignment exclusions = %#v", st.reassignExcludes)
	}
}

func TestImagesEnforceRequestBodyLimit(t *testing.T) {
	server, _, provider, plain := newTestServer(t)
	server.WithRequestBodyLimits(64, 256, time.Second)
	body := `{"model":"gpt-image-2","prompt":"` + strings.Repeat("x", 64) + `"}`

	recorder := serveGateway(t, server, plain, "/v1/images/generations", body)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if len(provider.imageGenerationBodies) != 0 {
		t.Fatalf("provider received oversized request: %#v", provider.imageGenerationBodies)
	}
}
