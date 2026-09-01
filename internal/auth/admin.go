package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const SessionCookieName = "subpool_session"

type session struct {
	expiresAt time.Time
}

type failureWindow struct {
	count   int
	resetAt time.Time
}

type AdminSessions struct {
	username string
	password string
	ttl      time.Duration
	secure   bool
	mu       sync.Mutex
	sessions map[string]session
	failures map[string]failureWindow
	now      func() time.Time
}

func NewAdminSessions(username, password string, ttl time.Duration, secure bool) *AdminSessions {
	return &AdminSessions{
		username: username, password: password, ttl: ttl, secure: secure,
		sessions: make(map[string]session), failures: make(map[string]failureWindow),
		now: time.Now,
	}
}

// Authenticate checks credentials in constant time and applies an in-memory failure limit.
func (a *AdminSessions) Authenticate(username, password, source string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	keys := []string{"ip:" + source, "credential:" + strings.ToLower(strings.TrimSpace(username)) + "|" + source}
	if len(a.failures) >= 10000 {
		for candidate, value := range a.failures {
			if !now.Before(value.resetAt) {
				delete(a.failures, candidate)
			}
		}
		for len(a.failures) >= 9000 {
			for candidate := range a.failures {
				delete(a.failures, candidate)
				break
			}
		}
	}
	for _, key := range keys {
		window := a.failures[key]
		if now.Before(window.resetAt) && window.count >= 5 {
			return "", false
		}
	}
	usernameHash, expectedUsernameHash := sha256.Sum256([]byte(username)), sha256.Sum256([]byte(a.username))
	passwordHash, expectedPasswordHash := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(a.password))
	validUser := subtle.ConstantTimeCompare(usernameHash[:], expectedUsernameHash[:]) == 1
	validPass := subtle.ConstantTimeCompare(passwordHash[:], expectedPasswordHash[:]) == 1
	if !validUser || !validPass {
		for _, key := range keys {
			window := a.failures[key]
			if !now.Before(window.resetAt) {
				window = failureWindow{resetAt: now.Add(time.Minute)}
			}
			window.count++
			a.failures[key] = window
		}
		return "", false
	}
	for _, key := range keys {
		delete(a.failures, key)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", false
	}
	id := base64.RawURLEncoding.EncodeToString(raw)
	a.sessions[id] = session{expiresAt: now.Add(a.ttl)}
	return id, true
}

func (a *AdminSessions) Valid(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.sessions[id]
	if !ok || !a.now().Before(s.expiresAt) {
		delete(a.sessions, id)
		return false
	}
	return true
}

func (a *AdminSessions) Revoke(id string) {
	a.mu.Lock()
	delete(a.sessions, id)
	a.mu.Unlock()
}

func (a *AdminSessions) SetCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: id, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: int(a.ttl.Seconds())})
}

func (a *AdminSessions) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

type SourceResolver struct {
	trusted []*net.IPNet
}

func NewSourceResolver(cidrs []string) (*SourceResolver, error) {
	resolver := &SourceResolver{}
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil {
			return nil, err
		}
		resolver.trusted = append(resolver.trusted, network)
	}
	return resolver, nil
}

func (s *SourceResolver) SourceIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote := net.ParseIP(strings.TrimSpace(host))
	if remote == nil || !s.isTrusted(remote) {
		return host
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for index := len(forwarded) - 1; index >= 0; index-- {
		candidate := net.ParseIP(strings.TrimSpace(forwarded[index]))
		if candidate != nil && !s.isTrusted(candidate) {
			return candidate.String()
		}
	}
	for _, raw := range forwarded {
		if candidate := net.ParseIP(strings.TrimSpace(raw)); candidate != nil {
			return candidate.String()
		}
	}
	return remote.String()
}

func (s *SourceResolver) isTrusted(ip net.IP) bool {
	for _, network := range s.trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
