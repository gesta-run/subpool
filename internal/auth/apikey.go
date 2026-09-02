package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
)

const keyPrefix = "sk-"

type APIKeys struct {
	hmacKey []byte
	random  io.Reader
}

func NewAPIKeys(hmacKey []byte) *APIKeys {
	return &APIKeys{hmacKey: append([]byte(nil), hmacKey...), random: rand.Reader}
}

func (a *APIKeys) Generate() (plain string, digest []byte, hint string, err error) {
	raw := make([]byte, 32)
	if _, err = io.ReadFull(a.random, raw); err != nil {
		return "", nil, "", fmt.Errorf("generate API key: %w", err)
	}
	plain = keyPrefix + base64.RawURLEncoding.EncodeToString(raw)
	digest = a.Digest(plain)
	hint = plain[len(plain)-4:]
	return plain, digest, hint, nil
}

func (a *APIKeys) Digest(plain string) []byte {
	mac := hmac.New(sha256.New, a.hmacKey)
	_, _ = mac.Write([]byte(plain))
	return mac.Sum(nil)
}

func Bearer(header string) (string, error) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], keyPrefix) {
		return "", fmt.Errorf("invalid bearer token")
	}
	return parts[1], nil
}
