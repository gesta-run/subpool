package control

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/auth"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/provider/codex"
)

func newControlServer(t *testing.T) (*Server, *controlStore, *credential.Cipher) {
	t.Helper()
	cipher, err := credential.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	st := &controlStore{}
	sessions := auth.NewAdminSessions("admin", "secret", time.Hour, false, bytes.Repeat([]byte{3}, 32))
	keys := auth.NewAPIKeys(bytes.Repeat([]byte{2}, 32))
	deviceAuth := &controlDeviceAuth{credentials: codex.Credentials{AccessToken: "access-token", RefreshToken: "refresh-token", AccountID: "upstream-account"}}
	sources, err := auth.NewSourceResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, sessions, keys, cipher, deviceAuth, &controlRefresher{}, sources).WithResetCredits(&controlResetCredits{}), st, cipher
}

func startDeviceLogin(t *testing.T, mux *http.ServeMux, cookie *http.Cookie) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/provider-accounts/codex/device-login", strings.NewReader(`{"display_name":"Imported account"}`))
	request.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func waitForDeviceLogin(t *testing.T, mux *http.ServeMux, cookie *http.Cookie) string {
	t.Helper()
	for range 100 {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/provider-accounts/codex/device-login/device-login", nil)
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status request=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != "pending" {
			return response.Status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("device login did not complete")
	return ""
}
func loginCookie(t *testing.T, mux *http.ServeMux) *http.Cookie {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"admin","password":"secret"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("login failed: %s", recorder.Body.String())
	}
	return recorder.Result().Cookies()[0]
}
