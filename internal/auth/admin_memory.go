package auth

import (
	"context"
	"sync"
	"time"
)

type memoryFailureWindow struct {
	count   int
	resetAt time.Time
}

type memoryAdminSessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	revoked  map[string]struct{}
	failures map[string]memoryFailureWindow
}

func newMemoryAdminSessionStore() *memoryAdminSessionStore {
	return &memoryAdminSessionStore{
		sessions: make(map[string]time.Time),
		revoked:  make(map[string]struct{}),
		failures: make(map[string]memoryFailureWindow),
	}
}

func (m *memoryAdminSessionStore) RecordAdminLoginAttempt(_ context.Context, keys []string, valid bool, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, key := range keys {
		window := m.failures[key]
		if now.Before(window.resetAt) && window.count >= 5 {
			return false, nil
		}
	}
	if valid {
		for _, key := range keys {
			delete(m.failures, key)
		}
		return true, nil
	}
	for _, key := range keys {
		window := m.failures[key]
		if !now.Before(window.resetAt) {
			window = memoryFailureWindow{resetAt: now.Add(time.Minute)}
		}
		window.count++
		m.failures[key] = window
	}
	return false, nil
}

func (m *memoryAdminSessionStore) CreateAdminSession(_ context.Context, digest []byte, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(digest)] = expiresAt
	delete(m.revoked, string(digest))
	return nil
}

func (m *memoryAdminSessionStore) AdminSessionActive(_ context.Context, digest []byte, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	expiresAt, exists := m.sessions[string(digest)]
	_, revoked := m.revoked[string(digest)]
	return exists && !revoked && now.Before(expiresAt), nil
}

func (m *memoryAdminSessionStore) RevokeAdminSession(_ context.Context, digest []byte, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.revoked[string(digest)] = struct{}{}
	return nil
}
