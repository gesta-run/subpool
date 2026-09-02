package config

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func setValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SUBPOOL_DATABASE_URL", "postgres://db/subpool")
	t.Setenv("SUBPOOL_PUBLIC_URL", "https://subpool.example.com")
	t.Setenv("SUBPOOL_ADMIN_USERNAME", "admin")
	t.Setenv("SUBPOOL_ADMIN_PASSWORD", "secret")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	t.Setenv("SUBPOOL_CREDENTIAL_KEY", key)
	t.Setenv("SUBPOOL_API_KEY_HMAC_KEY", key)
}
func TestLoad(t *testing.T) {
	setValidEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodexUpstreamURL != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("upstream = %s", cfg.CodexUpstreamURL)
	}
}
func TestLoadRejectsMissingAdminPassword(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_ADMIN_PASSWORD", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing password was accepted")
	}
}
func TestLoadRejectsInvalidEncryptionKey(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_CREDENTIAL_KEY", "short")
	if _, err := Load(); err == nil {
		t.Fatal("invalid key was accepted")
	}
}

func TestLoadTrustedProxyCIDRs(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_TRUSTED_PROXY_CIDRS", "10.0.0.0/8, 2001:db8::/32")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxyCIDRs) != 2 {
		t.Fatalf("trusted proxies = %#v", cfg.TrustedProxyCIDRs)
	}
}

func TestLoadRejectsInvalidTrustedProxyCIDR(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_TRUSTED_PROXY_CIDRS", "not-a-cidr")
	if _, err := Load(); err == nil {
		t.Fatal("invalid trusted proxy CIDR was accepted")
	}
}

func TestLoadRejectsPublicURLPathPrefix(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_PUBLIC_URL", "https://subpool.example.com/team-a")
	if _, err := Load(); err == nil {
		t.Fatal("expected a public URL path prefix error")
	}
}
