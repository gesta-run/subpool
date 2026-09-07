package httpclient

import (
	"net/http"
	"testing"
	"time"
)

func TestNewUsesLongResponseHeaderTimeout(t *testing.T) {
	assertResponseHeaderTimeout(t, New(), defaultResponseHeaderTimeout)
}

func TestNewWithResponseHeaderTimeout(t *testing.T) {
	assertResponseHeaderTimeout(t, NewWithResponseHeaderTimeout(4*time.Minute), 4*time.Minute)
}

func assertResponseHeaderTimeout(t *testing.T, client *http.Client, want time.Duration) {
	t.Helper()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.Transport)
	}
	if transport.ResponseHeaderTimeout != want {
		t.Fatalf("response header timeout = %s, want %s", transport.ResponseHeaderTimeout, want)
	}
}
