package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/id"
)

func TestPostgresAntigravitySignatureScopeExpiryAndCascade(t *testing.T) {
	url := os.Getenv("SUBPOOL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SUBPOOL_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	accountID, err := id.New()
	if err != nil {
		t.Fatal(err)
	}
	subject := sha256.Sum256([]byte(accountID))
	account := domain.ProviderAccount{ID: accountID, Provider: domain.ProviderAntigravity, CredentialType: domain.CredentialSubscription,
		DisplayName: "Example Google test account", SubjectHMAC: subject[:], CredentialCiphertext: []byte("example-encrypted-credential"), CredentialVersion: 1, Status: domain.AccountActive}
	if err = database.CreateProviderAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	defer database.DeleteProviderAccount(ctx, accountID)
	expected := []byte("example-encrypted-signature")
	if err = database.PutAntigravitySignature(ctx, accountID, "example-model", "example-call", expected, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	raw, err := database.GetAntigravitySignature(ctx, accountID, "example-model", "example-call")
	if err != nil || !bytes.Equal(raw, expected) {
		t.Fatalf("signature read failed: %v", err)
	}
	if _, err = database.GetAntigravitySignature(ctx, accountID, "other-model", "example-call"); err == nil {
		t.Fatal("signature crossed model scope")
	}
	if err = database.PutAntigravitySignature(ctx, accountID, "example-model", "example-call", expected, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = database.GetAntigravitySignature(ctx, accountID, "example-model", "example-call"); err == nil {
		t.Fatal("expired signature was returned")
	}
	if err = database.PutAntigravitySignature(ctx, accountID, "example-model", "example-call", expected, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = database.DeleteProviderAccount(ctx, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err = database.GetAntigravitySignature(ctx, accountID, "example-model", "example-call"); err == nil {
		t.Fatal("signature survived account deletion")
	}
}
