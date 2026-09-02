package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"net"
	"net/http"
	"strings"
	"time"
)

const SessionCookieName = "subpool_session"

const sessionPayloadSize = 1 + 8 + 32

type AdminSessionStore interface {
	RecordAdminLoginAttempt(context.Context, []string, bool, time.Time) (bool, error)
	CreateAdminSession(context.Context, []byte, time.Time) error
	AdminSessionActive(context.Context, []byte, time.Time) (bool, error)
	RevokeAdminSession(context.Context, []byte, time.Time) error
}

type AdminSessions struct {
	username string
	password string
	ttl      time.Duration
	secure   bool
	signKey  []byte
	store    AdminSessionStore
	now      func() time.Time
}

func NewAdminSessions(username, password string, ttl time.Duration, secure bool, signingSecret []byte, stores ...AdminSessionStore) *AdminSessions {
	keyMAC := hmac.New(sha256.New, signingSecret)
	_, _ = keyMAC.Write([]byte("subpool-admin-session-v1\x00" + username + "\x00" + password))
	var backend AdminSessionStore = newMemoryAdminSessionStore()
	if len(stores) > 0 && stores[0] != nil {
		backend = stores[0]
	}
	return &AdminSessions{
		username: username, password: password, ttl: ttl, secure: secure,
		signKey: keyMAC.Sum(nil), store: backend, now: time.Now,
	}
}

func (a *AdminSessions) Authenticate(ctx context.Context, username, password, source string) (string, bool, error) {
	now := a.now()
	keys := []string{"ip:" + source, "credential:" + strings.ToLower(strings.TrimSpace(username)) + "|" + source}
	usernameHash, expectedUsernameHash := sha256.Sum256([]byte(username)), sha256.Sum256([]byte(a.username))
	passwordHash, expectedPasswordHash := sha256.Sum256([]byte(password)), sha256.Sum256([]byte(a.password))
	validUser := subtle.ConstantTimeCompare(usernameHash[:], expectedUsernameHash[:]) == 1
	validPass := subtle.ConstantTimeCompare(passwordHash[:], expectedPasswordHash[:]) == 1
	accepted, err := a.store.RecordAdminLoginAttempt(ctx, keys, validUser && validPass, now)
	if err != nil || !accepted {
		return "", false, err
	}
	payload := make([]byte, sessionPayloadSize)
	payload[0] = 1
	expiresAt := now.Add(a.ttl)
	binary.BigEndian.PutUint64(payload[1:9], uint64(expiresAt.Unix()))
	if _, err := rand.Read(payload[9:]); err != nil {
		return "", false, err
	}
	id := a.encode(payload)
	if err = a.store.CreateAdminSession(ctx, sessionDigest(id), expiresAt); err != nil {
		return "", false, err
	}
	return id, true, nil
}

func (a *AdminSessions) Valid(ctx context.Context, id string) (bool, error) {
	expiresAt, ok := a.verify(id)
	if !ok {
		return false, nil
	}
	now := a.now()
	if !now.Before(expiresAt) {
		return false, nil
	}
	return a.store.AdminSessionActive(ctx, sessionDigest(id), now)
}

func (a *AdminSessions) Revoke(ctx context.Context, id string) error {
	_, ok := a.verify(id)
	if !ok {
		return nil
	}
	return a.store.RevokeAdminSession(ctx, sessionDigest(id), a.now())
}

func (a *AdminSessions) encode(payload []byte) string {
	mac := hmac.New(sha256.New, a.signKey)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (a *AdminSessions) verify(id string) (time.Time, bool) {
	parts := strings.Split(id, ".")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	payload, payloadErr := base64.RawURLEncoding.DecodeString(parts[0])
	signature, signatureErr := base64.RawURLEncoding.DecodeString(parts[1])
	if payloadErr != nil || signatureErr != nil || len(payload) != sessionPayloadSize || payload[0] != 1 || len(signature) != sha256.Size {
		return time.Time{}, false
	}
	mac := hmac.New(sha256.New, a.signKey)
	_, _ = mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return time.Time{}, false
	}
	expiresAt := time.Unix(int64(binary.BigEndian.Uint64(payload[1:9])), 0)
	return expiresAt, true
}

func sessionDigest(id string) []byte {
	digest := sha256.Sum256([]byte(id))
	return digest[:]
}

func (a *AdminSessions) SetCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: id, Path: "/", HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteStrictMode, MaxAge: int(a.ttl.Seconds()), Expires: a.now().Add(a.ttl)})
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
