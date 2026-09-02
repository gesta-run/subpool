package openaicompat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatCompletionsUsesConfiguredBaseURLAndBearerKey(t *testing.T) {
	var path, authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		authorization = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	response, err := NewClient(server.Client()).ChatCompletions(context.Background(), []byte(`{"model":"test"}`), nil, Credentials{BaseURL: server.URL + "/compatible/v1", APIKey: "sk-test-placeholder"})
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if path != "/compatible/v1/chat/completions" || authorization != "Bearer sk-test-placeholder" {
		t.Fatalf("path=%q authorization=%q", path, authorization)
	}
}
