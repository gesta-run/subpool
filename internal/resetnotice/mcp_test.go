package resetnotice

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMCPReceiptsAndErrors(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprint(sse), func(t *testing.T) {
			calls := 0
			fail := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer illustrative" {
					t.Error("authorization missing")
				}
				var req struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
					Params struct {
						Name      string            `json:"name"`
						Arguments map[string]string `json:"arguments"`
					} `json:"params"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				var result any
				switch req.Method {
				case "initialize":
					w.Header().Set("Mcp-Session-Id", "test-session")
					result = map[string]string{"protocolVersion": "2025-03-26"}
				case "notifications/initialized":
					w.WriteHeader(202)
					return
				case "tools/call":
					calls++
					if r.Header.Get("Mcp-Session-Id") != "test-session" || req.Params.Name != "slack__slack_send_message" || req.Params.Arguments["channel_id"] != "illustrative-channel" {
						t.Error("wrong session or tool target")
					}
					result = map[string]any{"isError": fail, "content": []map[string]string{{"type": "text", "text": `{"message_link":"https://example.com/receipt"}`}}}
				default:
					t.Error("unexpected method")
				}
				payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
				if sse {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Write(payload)
				}
			}))
			defer srv.Close()
			m := &MCP{Config: Config{URL: srv.URL, Authorization: "Bearer illustrative", Tool: "slack__slack_send_message"}, Client: srv.Client()}
			link, err := m.Send(context.Background(), "illustrative-channel", "Illustrative message")
			if err != nil || link != "https://example.com/receipt" || calls != 1 {
				t.Fatal(link, err, calls)
			}
			fail = true
			if _, err = m.Send(context.Background(), "illustrative-channel", "Illustrative message"); err == nil {
				t.Fatal("tool failure treated as delivered")
			}
		})
	}
}
