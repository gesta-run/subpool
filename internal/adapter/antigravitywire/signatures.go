package antigravitywire

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

type signature struct {
	value   string
	digest  [32]byte
	expires time.Time
	prefix  json.RawMessage
}

// Signatures is bounded, account/model scoped protocol state. Missing signatures
// fail closed instead of sending fabricated signatures to Google.
type Signatures struct {
	mu      sync.Mutex
	entries map[string]signature
	bytes   int
	storage SignatureStorage
	cipher  StateCipher
}

func NewSignatures() *Signatures { return &Signatures{entries: make(map[string]signature)} }

func signatureKey(scope, model, id string) string { return scope + "\x00" + model + "\x00" + id }
func callDigest(name string, args any) [32]byte {
	raw, _ := json.Marshal(args)
	return sha256.Sum256(append([]byte(name+"\x00"), raw...))
}

func (s *Signatures) Save(scope, model, id, name string, args any, value string, prefix ...[]any) error {
	if s == nil {
		return nil
	}
	if len(value) > 128<<10 {
		return errors.New("Antigravity signature exceeds size limit")
	}
	entry := signature{value: value, digest: callDigest(name, args), expires: time.Now().Add(24 * time.Hour)}
	if len(prefix) > 0 && len(prefix[0]) > 0 {
		entry.prefix, _ = json.Marshal(prefix[0])
		if len(entry.prefix) > 64<<10 {
			return errors.New("Antigravity native assistant state exceeds size limit")
		}
	}
	if err := s.persist(scope, model, id, entry); err != nil {
		return err
	}
	s.remember(signatureKey(scope, model, id), entry)
	return nil
}

func (s *Signatures) remember(key string, entry signature) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.entries[key]; ok {
		s.bytes -= len(old.value) + len(old.prefix)
		delete(s.entries, key)
	}
	for entryKey, old := range s.entries {
		if time.Now().After(old.expires) || len(s.entries) >= 4096 || s.bytes+len(entry.value)+len(entry.prefix) > 32<<20 {
			s.bytes -= len(old.value) + len(old.prefix)
			delete(s.entries, entryKey)
		}
	}
	s.entries[key] = entry
	s.bytes += len(entry.value) + len(entry.prefix)
}

func (s *Signatures) Lookup(scope, model, id, name string, args any) (string, bool) {
	value, _, ok := s.History(scope, model, id, name, args)
	return value, ok
}

func (s *Signatures) History(scope, model, id, name string, args any) (string, json.RawMessage, bool) {
	if s == nil {
		return "", nil, false
	}
	s.mu.Lock()
	entry, ok := s.entries[signatureKey(scope, model, id)]
	s.mu.Unlock()
	if !ok {
		var err error
		entry, err = s.load(scope, model, id)
		ok = err == nil
		if ok {
			s.remember(signatureKey(scope, model, id), entry)
		}
	}
	return entry.value, entry.prefix, ok && time.Now().Before(entry.expires) && entry.digest == callDigest(name, args)
}
