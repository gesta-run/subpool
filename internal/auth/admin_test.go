package auth

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminSessionLifecycle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	backend := newMemoryAdminSessionStore()
	sessions := NewAdminSessions("admin", "secret", time.Hour, true, bytes.Repeat([]byte{1}, 32), backend)
	sessions.now = func() time.Time { return now }
	if _, ok, err := sessions.Authenticate(context.Background(), "admin", "wrong", "127.0.0.1"); err != nil || ok {
		t.Fatal("wrong password authenticated")
	}
	id, ok, err := sessions.Authenticate(context.Background(), "admin", "secret", "127.0.0.1")
	if err != nil || !ok || id == "" {
		t.Fatal("valid credentials were rejected")
	}
	if valid, _ := sessions.Valid(context.Background(), id); !valid {
		t.Fatal("session is not valid")
	}
	restarted := NewAdminSessions("admin", "secret", time.Hour, true, bytes.Repeat([]byte{1}, 32), backend)
	restarted.now = func() time.Time { return now }
	if valid, _ := restarted.Valid(context.Background(), id); !valid {
		t.Fatal("session did not survive a process restart")
	}
	changedPassword := NewAdminSessions("admin", "changed", time.Hour, true, bytes.Repeat([]byte{1}, 32), backend)
	changedPassword.now = func() time.Time { return now }
	if valid, _ := changedPassword.Valid(context.Background(), id); valid {
		t.Fatal("session remained valid after credentials changed")
	}
	recorder := httptest.NewRecorder()
	sessions.SetCookie(recorder, id)
	cookie := recorder.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != 3 {
		t.Fatalf("insecure cookie: %#v", cookie)
	}
	if valid, _ := sessions.Valid(context.Background(), id+"tampered"); valid {
		t.Fatal("tampered session was accepted")
	}
	if err = sessions.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if valid, _ := sessions.Valid(context.Background(), id); valid {
		t.Fatal("revoked session remained valid")
	}
	now = now.Add(2 * time.Hour)
	if valid, _ := restarted.Valid(context.Background(), id); valid {
		t.Fatal("expired session remained valid")
	}
}

func TestAdminLoginFailureLimit(t *testing.T) {
	sessions := NewAdminSessions("admin", "secret", time.Hour, false, bytes.Repeat([]byte{1}, 32))
	for i := 0; i < 5; i++ {
		_, _, _ = sessions.Authenticate(context.Background(), "guessed-user-"+string(rune('a'+i)), "bad", "ip")
	}
	if _, ok, _ := sessions.Authenticate(context.Background(), "admin", "secret", "ip"); ok {
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
