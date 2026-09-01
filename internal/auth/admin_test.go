package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminSessionLifecycle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sessions := NewAdminSessions("admin", "secret", time.Hour, true)
	sessions.now = func() time.Time { return now }
	if _, ok := sessions.Authenticate("admin", "wrong", "127.0.0.1"); ok {
		t.Fatal("wrong password authenticated")
	}
	id, ok := sessions.Authenticate("admin", "secret", "127.0.0.1")
	if !ok || id == "" {
		t.Fatal("valid credentials were rejected")
	}
	if !sessions.Valid(id) {
		t.Fatal("session is not valid")
	}
	recorder := httptest.NewRecorder()
	sessions.SetCookie(recorder, id)
	cookie := recorder.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != 3 {
		t.Fatalf("insecure cookie: %#v", cookie)
	}
	now = now.Add(2 * time.Hour)
	if sessions.Valid(id) {
		t.Fatal("expired session remained valid")
	}
}

func TestAdminLoginFailureLimit(t *testing.T) {
	sessions := NewAdminSessions("admin", "secret", time.Hour, false)
	for i := 0; i < 5; i++ {
		sessions.Authenticate("guessed-user-"+string(rune('a'+i)), "bad", "ip")
	}
	if _, ok := sessions.Authenticate("admin", "secret", "ip"); ok {
		t.Fatal("failure limit was not enforced")
	}
}

func TestSourceResolverIgnoresUntrustedForwardedFor(t *testing.T) {
	resolver, err := NewSourceResolver(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := resolver.SourceIP(request); got != "192.0.2.10" {
		t.Fatalf("source = %q", got)
	}
}

func TestSourceResolverUsesTrustedForwardedChain(t *testing.T) {
	resolver, err := NewSourceResolver([]string{"10.0.0.0/8", "192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = "10.0.0.4:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9, 192.0.2.7")
	if got := resolver.SourceIP(request); got != "203.0.113.9" {
		t.Fatalf("source = %q", got)
	}
}
