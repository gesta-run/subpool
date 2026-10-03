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
		for name, want := range map[string]string{"Authorization": "Bearer access", "Chatgpt-Account-Id": "acct", "Originator": "codex_cli_rs", "Accept": "text/event-stream", "Session-Id": "session", "X-Codex-Installation-Id": "installation"} {
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
	headers := http.Header{"Session-Id": []string{"session"}, "X-Codex-Installation-Id": []string{"installation"}}
	resp, err := client.Responses(context.Background(), []byte(`{"model":"gpt-test"}`), headers, Credentials{AccessToken: "access", AccountID: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestClientImages(t *testing.T) {
	for _, test := range []struct {
		name string
		path string
		call func(*Client, http.Header) (*http.Response, error)
	}{
		{
			name: "generations",
			path: "/images/generations",
			call: func(client *Client, headers http.Header) (*http.Response, error) {
				return client.ImageGenerations(context.Background(), []byte(`{"model":"gpt-image-2"}`), headers, Credentials{AccessToken: "access", AccountID: "acct"})
			},
		},
		{
			name: "edits",
			path: "/images/edits",
			call: func(client *Client, headers http.Header) (*http.Response, error) {
				return client.ImageEdits(context.Background(), []byte(`{"model":"gpt-image-2"}`), headers, Credentials{AccessToken: "access", AccountID: "acct"})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					t.Errorf("path = %s, want %s", r.URL.Path, test.path)
				}
				for name, want := range map[string]string{
					"Authorization":           "Bearer access",
					"Chatgpt-Account-Id":      "acct",
					"Content-Type":            "application/json",
					"Accept":                  "application/json",
					"Originator":              "codex_cli_rs",
					"X-Codex-Installation-Id": "installation",
					"X-Client-Request-Id":     "request",
				} {
					if got := r.Header.Get(name); got != want {
						t.Errorf("%s = %q, want %q", name, got, want)
					}
				}
				body, _ := io.ReadAll(r.Body)
				if string(body) != `{"model":"gpt-image-2"}` {
					t.Errorf("body = %s", body)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[]}`))
			}))
			defer server.Close()

			client := NewClient(server.URL, http.DefaultClient)
			resp, err := test.call(client, http.Header{
				"X-Codex-Installation-Id": []string{"installation"},
				"X-Client-Request-Id":     []string{"request"},
			})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		})
	}
}

func TestSetRoutingHint(t *testing.T) {
	headers := http.Header{"x-codex-routing-hint": {"spoofed"}}
	SetRoutingHint(headers, "gpt-5.6-sol", true)
	if got := headers.Get(RoutingHintHeader); got != "model=gpt-5.6-sol;tier=priority" {
		t.Fatalf("routing hint = %q", got)
	}
	SetRoutingHint(headers, "gpt-5.6-sol", false)
	if got := headers.Get(RoutingHintHeader); got != "model=gpt-5.6-sol" {
		t.Fatalf("standard routing hint = %q", got)
	}
	SetRoutingHint(headers, "invalid;model", false)
	if got := headers.Get(RoutingHintHeader); got != "" {
		t.Fatalf("invalid model routing hint = %q", got)
	}
}
