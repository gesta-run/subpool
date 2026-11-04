package modelusage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientValidatesWindowCachesAndKeepsAuthorizationPrivate(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-time.Hour)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer illustrative-secret" || r.URL.Query().Get("start") != start.Format(time.RFC3339Nano) || r.URL.Query().Get("end") != end.Format(time.RFC3339Nano) {
			t.Error("wrong authentication or window")
		}
		json.NewEncoder(w).Encode(Snapshot{Start: start, End: end, GeneratedAt: end, Employees: []Employee{{ID: "alice", Name: "Alice", Models: []Model{{Model: "gpt-6.1-sol", Turns: 8}}}}})
	}))
	defer server.Close()
	c := &Client{URL: server.URL, Token: "illustrative-secret", HTTP: server.Client()}
	for i := 0; i < 2; i++ {
		s, err := c.Read(context.Background(), start, end)
		if err != nil || len(s.Employees) != 1 {
			t.Fatalf("read: %+v %v", s, err)
		}
	}
	if calls != 1 {
		t.Fatalf("identical window fetched %d times", calls)
	}
}

func TestClientRejectsInvalidAndRedirectedFeeds(t *testing.T) {
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-time.Hour)
	for _, mode := range []string{"wrong-window", "stale", "negative", "duplicate", "unavailable", "redirect", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s := Snapshot{Start: start, End: end, GeneratedAt: end, Employees: []Employee{{ID: "alice", Models: []Model{{Model: "sol", Turns: 8}}}}}
				switch mode {
				case "wrong-window":
					s.Start = start.Add(-time.Hour)
				case "stale":
					s.GeneratedAt = end.Add(-time.Hour)
				case "negative":
					s.Employees[0].Models[0].Turns = -1
				case "duplicate":
					s.Employees = append(s.Employees, s.Employees[0])
				case "unavailable":
					w.WriteHeader(503)
					w.Write([]byte("illustrative-secret"))
					return
				case "redirect":
					http.Redirect(w, r, "https://example.test/secret", 302)
					return
				case "invalid-json":
					w.Write([]byte("illustrative-secret"))
					return
				}
				json.NewEncoder(w).Encode(s)
			}))
			defer server.Close()
			c := &Client{URL: server.URL, Token: "illustrative-secret", HTTP: server.Client()}
			if _, err := c.Read(context.Background(), start, end); err == nil || strings.Contains(err.Error(), "illustrative-secret") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
