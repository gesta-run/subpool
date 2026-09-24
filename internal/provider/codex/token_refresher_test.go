package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOAuthRefreshPreservesRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("scope") != "" {
			t.Error("refresh request must not send scope")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "expires_in": 60})
	}))
	defer server.Close()
	refresher := NewTokenRefresher(TokenRefresherConfig{ClientID: "client", TokenURL: server.URL})
	credentials, err := refresher.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatal(err)
	}
	if credentials.RefreshToken != "old-refresh" {
		t.Fatalf("refresh token = %q", credentials.RefreshToken)
	}
}

func TestRefreshErrorClassification(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body       string
		definitive bool
	}{
		{name: "invalid grant", status: http.StatusBadRequest, body: `{"error":"invalid_grant"}`, definitive: true},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"code":"expired_token"}}`, definitive: true},
		{name: "server error", status: http.StatusBadGateway, body: `{"error":"temporarily_unavailable"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			refresher := NewTokenRefresher(TokenRefresherConfig{ClientID: "client", TokenURL: server.URL})
			_, err := refresher.Refresh(context.Background(), "refresh")
			if err == nil || IsDefinitiveAuthError(err) != test.definitive {
				t.Fatalf("error=%v definitive=%v", err, IsDefinitiveAuthError(err))
			}
		})
	}
}
