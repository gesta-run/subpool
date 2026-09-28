package copilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDeviceAuthCompletesGitHubFlow(t *testing.T) {
	var polls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			_, _ = fmt.Fprintf(w, `{"device_code":"device-secret","user_code":"ABCD-EFGH","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`)
		case "/access":
			polls++
			if polls == 1 {
				_, _ = fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"access_token":"github-secret"}`)
		case "/exchange":
			if r.Header.Get("Authorization") != "token github-secret" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = fmt.Fprintf(w, `{"token":"copilot-secret","expires_at":%d,"refresh_in":1200}`, time.Now().Add(30*time.Minute).Unix())
		case "/user":
			_, _ = fmt.Fprint(w, `{"login":"octocat","id":42,"email":"octocat@example.com"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	auth := newTestDeviceAuth(server)
	auth.wait = func(context.Context, time.Duration) error { return nil }
	defer auth.Close()
	authorization, results, err := auth.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if authorization.UserCode != "ABCD-EFGH" || authorization.VerificationURL != "https://github.com/login/device" {
		t.Fatalf("authorization = %#v", authorization)
	}
	result := <-results
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.Credentials.Login != "octocat" || result.Credentials.UserID != 42 || result.Credentials.GitHubToken != "github-secret" {
		t.Fatalf("credentials = %#v", result.Credentials)
	}
}

func TestDeviceAuthHonorsSlowDown(t *testing.T) {
	statuses := []string{"authorization_pending", "slow_down", ""}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			_, _ = fmt.Fprint(w, `{"device_code":"secret","user_code":"CODE","verification_uri":"https://github.com/login/device","expires_in":900,"interval":2}`)
		case "/access":
			status := statuses[0]
			statuses = statuses[1:]
			if status == "" {
				_, _ = fmt.Fprint(w, `{"access_token":"github-secret"}`)
			} else {
				_, _ = fmt.Fprintf(w, `{"error":%q}`, status)
			}
		case "/exchange":
			_, _ = fmt.Fprintf(w, `{"token":"copilot-secret","expires_at":%d}`, time.Now().Add(30*time.Minute).Unix())
		case "/user":
			_, _ = fmt.Fprint(w, `{"login":"octocat","id":42}`)
		}
	}))
	defer server.Close()
	auth := newTestDeviceAuth(server)
	var mu sync.Mutex
	var waits []time.Duration
	auth.wait = func(_ context.Context, duration time.Duration) error {
		mu.Lock()
		waits = append(waits, duration)
		mu.Unlock()
		return nil
	}
	defer auth.Close()
	_, results, err := auth.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result := <-results; result.Err != nil {
		t.Fatal(result.Err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(waits, []time.Duration{2 * time.Second, 2 * time.Second, 7 * time.Second}) {
		t.Fatalf("waits = %v", waits)
	}
}

func TestDeviceAuthSendsExpectedRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values, _ := url.ParseQuery(func() string {
			buffer := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(buffer)
			return string(buffer)
		}())
		if values.Get("client_id") != "test-client" || values.Get("scope") != "read:user" {
			t.Errorf("values = %#v", values)
		}
		_, _ = fmt.Fprint(w, `{"device_code":"secret","user_code":"CODE","verification_uri":"https://github.com/login/device","expires_in":900,"interval":2}`)
	}))
	defer server.Close()
	auth := newTestDeviceAuth(server)
	auth.config.ClientID = "test-client"
	defer auth.Close()
	_, _, err := auth.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeviceAuthExposesSafeDeviceCodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"message":"Device authorization is disabled.","token":"private-value"}`)
	}))
	defer server.Close()
	auth := newTestDeviceAuth(server)
	defer auth.Close()

	_, _, err := auth.Start(context.Background())
	if err == nil || SafeErrorDetail(err) != "Device authorization is disabled." || strings.Contains(err.Error(), "private-value") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeviceAuthExposesDeniedAuthorization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/device":
			_, _ = fmt.Fprint(w, `{"device_code":"secret","user_code":"CODE","verification_uri":"https://github.com/login/device","expires_in":900,"interval":1}`)
		case "/access":
			_, _ = fmt.Fprint(w, `{"error":"access_denied","error_description":"The authorization request was denied."}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	auth := newTestDeviceAuth(server)
	auth.wait = func(context.Context, time.Duration) error { return nil }
	defer auth.Close()

	_, results, err := auth.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := <-results
	if result.Err == nil || SafeErrorDetail(result.Err) != "The authorization request was denied." {
		t.Fatalf("error = %v", result.Err)
	}
}

func newTestDeviceAuth(server *httptest.Server) *DeviceAuth {
	return NewDeviceAuth(DeviceAuthConfig{
		ClientID: "test-client", DeviceCodeURL: server.URL + "/device", AccessTokenURL: server.URL + "/access",
		UserURL: server.URL + "/user", TokenExchangeURL: server.URL + "/exchange", HTTPClient: server.Client(),
	})
}
