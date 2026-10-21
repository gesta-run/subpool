package antigravitywire

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type SignatureStorage interface {
	PutAntigravitySignature(context.Context, string, string, string, []byte, time.Time) error
	GetAntigravitySignature(context.Context, string, string, string) ([]byte, error)
}

type StateCipher interface {
	Seal([]byte, []byte) ([]byte, error)
	Open([]byte, []byte) ([]byte, error)
}

type storedSignature struct {
	Value                string
	Digest               [32]byte
	Expires              time.Time
	RequiresNativePrefix bool
}

func (s *Signatures) WithStorage(storage SignatureStorage, cipher StateCipher) *Signatures {
	s.storage, s.cipher = storage, cipher
	return s
}

func (s *Signatures) persist(scope, model, id string, entry signature) error {
	if s.storage == nil {
		return nil
	}
	// Persist opaque signatures and fingerprints, never native assistant text.
	raw, err := json.Marshal(storedSignature{Value: entry.value, Digest: entry.digest, Expires: entry.expires, RequiresNativePrefix: len(entry.prefix) > 0})
	if err != nil {
		return err
	}
	sealed, err := s.cipher.Seal(raw, []byte("antigravity-signature:v1\x00"+signatureKey(scope, model, id)))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.storage.PutAntigravitySignature(ctx, scope, model, id, sealed, entry.expires)
}

func (s *Signatures) load(scope, model, id string) (signature, error) {
	if s.storage == nil {
		return signature{}, errors.New("signature not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	raw, err := s.storage.GetAntigravitySignature(ctx, scope, model, id)
	if err != nil {
		return signature{}, err
	}
	plain, err := s.cipher.Open(raw, []byte("antigravity-signature:v1\x00"+signatureKey(scope, model, id)))
	if err != nil {
		return signature{}, err
	}
	var stored storedSignature
	if json.Unmarshal(plain, &stored) != nil || time.Now().After(stored.Expires) || len(stored.Value) > 128<<10 || stored.RequiresNativePrefix {
		return signature{}, errors.New("invalid or expired signature")
	}
	return signature{value: stored.Value, digest: stored.Digest, expires: stored.Expires}, nil
}
