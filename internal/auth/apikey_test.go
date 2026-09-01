package auth

import (
	"bytes"
	"strings"
	"testing"
)

func TestAPIKeyGenerationAndDigest(t *testing.T) {
	keys := NewAPIKeys(bytes.Repeat([]byte{1}, 32))
	plain, digest, hint, err := keys.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plain, "sk-subpool-") {
		t.Fatalf("unexpected prefix: %s", plain)
	}
	if strings.Contains(plain, ".") {
		t.Fatalf("API key must be a single opaque segment: %s", plain)
	}
	if len(plain) != (len("sk-subpool-") + 43) {
		t.Fatalf("unexpected API key length: %d", len(plain))
	}
	if hint != plain[len(plain)-4:] {
		t.Fatalf("hint = %q", hint)
	}
	if !bytes.Equal(digest, keys.Digest(plain)) {
		t.Fatal("digest is not stable")
	}
}

func TestBearer(t *testing.T) {
	key := "sk-subpool-example"
	if got, err := Bearer("Bearer " + key); err != nil || got != key {
		t.Fatalf("Bearer() = %q, %v", got, err)
	}
	for _, value := range []string{"", key, "Basic " + key, "Bearer sk-other-value"} {
		if _, err := Bearer(value); err == nil {
			t.Fatalf("Bearer(%q) succeeded", value)
		}
	}
}
