package credential

import (
	"bytes"
	"testing"
)

func TestCipherRoundTripAndTamperDetection(t *testing.T) {
	cipher, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"refresh_token":"example"}`)
	encrypted, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, plaintext) {
		t.Fatal("ciphertext contains plaintext")
	}
	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Decrypt() = %q, %v", decrypted, err)
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err = cipher.Decrypt(encrypted); err == nil {
		t.Fatal("tampered ciphertext decrypted")
	}
}
