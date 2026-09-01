package codex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		for name, want := range map[string]string{"Authorization": "Bearer access", "Chatgpt-Account-Id": "acct", "Originator": "codex_cli_rs", "Accept": "text/event-stream", "Session-Id": "session"} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"model":"gpt-test"}` {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewClient(server.URL, http.DefaultClient)
	headers := http.Header{"Session-Id": []string{"session"}}
	resp, err := client.Responses(context.Background(), []byte(`{"model":"gpt-test"}`), headers, Credentials{AccessToken: "access", AccountID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
