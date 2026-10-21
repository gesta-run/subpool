package antigravitywire

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
)

type signatureMemoryStore struct {
	data map[string][]byte
	fail bool
}

func (s *signatureMemoryStore) PutAntigravitySignature(_ context.Context, account, model, id string, raw []byte, _ time.Time) error {
	if s.fail {
		return errors.New("example unavailable storage")
	}
	s.data[signatureKey(account, model, id)] = append([]byte(nil), raw...)
	return nil
}
func (s *signatureMemoryStore) GetAntigravitySignature(_ context.Context, account, model, id string) ([]byte, error) {
	raw, ok := s.data[signatureKey(account, model, id)]
	if !ok {
		return nil, errors.New("not found")
	}
	return raw, nil
}

func TestDurableSignaturesSurviveCacheRestartAndRejectScopeTampering(t *testing.T) {
	cipher, err := credential.New(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	backend := &signatureMemoryStore{data: map[string][]byte{}}
	first := NewSignatures().WithStorage(backend, cipher)
	args := map[string]any{"id": "example"}
	if err = first.Save("a", "m", "call", "lookup", args, "example-private-signature"); err != nil {
		t.Fatal(err)
	}
	raw := backend.data[signatureKey("a", "m", "call")]
	if bytes.Contains(raw, []byte("example-private-signature")) {
		t.Fatal("signature persisted as plaintext")
	}
	second := NewSignatures().WithStorage(backend, cipher)
	if sig, ok := second.Lookup("a", "m", "call", "lookup", args); !ok || sig != "example-private-signature" {
		t.Fatal("signature did not survive process cache restart")
	}
	backend.data[signatureKey("b", "m", "call")] = raw
	if _, ok := second.Lookup("b", "m", "call", "lookup", args); ok {
		t.Fatal("ciphertext transplanted across account scopes")
	}
	if _, ok := second.Lookup("a", "m", "call", "lookup", map[string]any{"id": "changed"}); ok {
		t.Fatal("signed arguments could be modified")
	}
	backend.fail = true
	if err = first.Save("a", "m", "new", "lookup", args, "signature"); err == nil {
		t.Fatal("signature persistence failure was hidden")
	}
}

func TestParallelToolCallsPreserveUnsignedPartsWithoutFabrication(t *testing.T) {
	signatures := NewSignatures()
	if err := signatures.Save("a", "gemini-3-flash", "call", "lookup", map[string]any{}, ""); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call","content":"example"}]}`)
	raw, err := Request(input, "a", "gemini-3-flash", signatures)
	if err != nil || bytes.Contains(raw, []byte("thoughtSignature")) {
		t.Fatalf("signature was fabricated: %s %v", raw, err)
	}
}

func TestNativeThinkingStateIsNeverPersistedAsTranscript(t *testing.T) {
	cipher, _ := credential.New(bytes.Repeat([]byte{5}, 32))
	backend := &signatureMemoryStore{data: map[string][]byte{}}
	cache := NewSignatures().WithStorage(backend, cipher)
	thoughts := []any{
		map[string]any{"thought": true, "text": "example native thinking text", "thoughtSignature": "example-native-signature"},
		map[string]any{"text": "example visible assistant text"},
	}
	if err := cache.Save("a", "claude-thinking", "call", "lookup", map[string]any{}, "", thoughts); err != nil {
		t.Fatal(err)
	}
	key := signatureKey("a", "claude-thinking", "call")
	plain, err := cipher.Open(backend.data[key], []byte("antigravity-signature:v1\x00"+key))
	if err != nil || bytes.Contains(plain, []byte("example native thinking text")) || bytes.Contains(plain, []byte("example visible assistant text")) {
		t.Fatal("native assistant text was persisted")
	}
	var stored storedSignature
	if json.Unmarshal(plain, &stored) != nil || !stored.RequiresNativePrefix {
		t.Fatal("volatile native history requirement was not recorded")
	}
	input := []byte(`{"messages":[{"role":"assistant","content":"example visible assistant text","tool_calls":[{"id":"call","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call","content":"example result"}]}`)
	raw, err := Request(input, "a", "claude-thinking", cache)
	if err != nil || !bytes.Contains(raw, []byte("example native thinking text")) || !bytes.Contains(raw, []byte("example-native-signature")) {
		t.Fatalf("native thought state was not restored: %s %v", raw, err)
	}
	restarted := NewSignatures().WithStorage(backend, cipher)
	if _, err := Request(input, "a", "claude-thinking", restarted); err == nil {
		t.Fatal("missing volatile thinking state was silently ignored")
	}
}
