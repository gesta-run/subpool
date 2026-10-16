package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

type Cipher struct {
	aead cipher.AEAD
}

func New(key []byte) (*Cipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential AEAD: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Encrypt(plaintext []byte) ([]byte, error) {
	return c.Seal(plaintext, nil)
}

// Seal authenticates a purpose and caller scope separately from credentials.
func (c *Cipher) Seal(plaintext, associatedData []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate credential nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, associatedData), nil
}

func (c *Cipher) Decrypt(ciphertext []byte) ([]byte, error) {
	return c.Open(ciphertext, nil)
}

func (c *Cipher) Open(ciphertext, associatedData []byte) ([]byte, error) {
	if len(ciphertext) < c.aead.NonceSize() {
		return nil, fmt.Errorf("credential ciphertext is invalid")
	}
	nonce := ciphertext[:c.aead.NonceSize()]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext[c.aead.NonceSize():], associatedData)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential: %w", err)
	}
	return plaintext, nil
}
