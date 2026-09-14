package config

import (
	"bytes"
	"encoding/base64"
	"testing"
	"time"
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
	t.Setenv("SUBPOOL_MAX_REQUEST_BODY_BYTES", "")
	t.Setenv("SUBPOOL_MAX_INFLIGHT_REQUEST_BODY_BYTES", "")
	t.Setenv("SUBPOOL_REQUEST_BODY_READ_TIMEOUT", "")
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
	if !cfg.ResponsesWSEnabled {
		t.Fatal("Responses WebSocket should be enabled by default")
	}
	if cfg.UpstreamResponseHeaderTimeout != 3*time.Minute {
		t.Fatalf("upstream response header timeout = %s", cfg.UpstreamResponseHeaderTimeout)
	}
	if cfg.MaxRequestBodyBytes != 256<<20 || cfg.MaxInflightRequestBodyBytes != 1<<30 {
		t.Fatalf("request body limits = %d/%d", cfg.MaxRequestBodyBytes, cfg.MaxInflightRequestBodyBytes)
	}
	if cfg.RequestBodyReadTimeout != 5*time.Minute {
		t.Fatalf("request body read timeout = %s", cfg.RequestBodyReadTimeout)
	}
}

func TestLoadRequestBodyLimitOverrides(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_MAX_REQUEST_BODY_BYTES", "1048576")
	t.Setenv("SUBPOOL_MAX_INFLIGHT_REQUEST_BODY_BYTES", "4194304")
	t.Setenv("SUBPOOL_REQUEST_BODY_READ_TIMEOUT", "7m")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRequestBodyBytes != 1048576 || cfg.MaxInflightRequestBodyBytes != 4194304 {
		t.Fatalf("request body limits = %d/%d", cfg.MaxRequestBodyBytes, cfg.MaxInflightRequestBodyBytes)
	}
	if cfg.RequestBodyReadTimeout != 7*time.Minute {
		t.Fatalf("request body read timeout = %s", cfg.RequestBodyReadTimeout)
	}
}

func TestLoadRejectsInvalidRequestBodyLimits(t *testing.T) {
	tests := []struct {
		name     string
		request  string
		inflight string
	}{
		{name: "non-numeric request limit", request: "large", inflight: "4194304"},
		{name: "zero request limit", request: "0", inflight: "4194304"},
		{name: "aggregate below buffer requirement", request: "2097152", inflight: "4194304"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv("SUBPOOL_MAX_REQUEST_BODY_BYTES", test.request)
			t.Setenv("SUBPOOL_MAX_INFLIGHT_REQUEST_BODY_BYTES", test.inflight)
			if _, err := Load(); err == nil {
				t.Fatal("invalid request body limits were accepted")
			}
		})
	}
}

func TestLoadRejectsInvalidRequestBodyReadTimeout(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_REQUEST_BODY_READ_TIMEOUT", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("invalid request body read timeout was accepted")
	}
}

func TestLoadUpstreamResponseHeaderTimeoutOverride(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_UPSTREAM_RESPONSE_HEADER_TIMEOUT", "4m30s")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UpstreamResponseHeaderTimeout != 4*time.Minute+30*time.Second {
		t.Fatalf("upstream response header timeout = %s", cfg.UpstreamResponseHeaderTimeout)
	}
}

func TestLoadRejectsInvalidUpstreamResponseHeaderTimeout(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_UPSTREAM_RESPONSE_HEADER_TIMEOUT", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("invalid upstream response header timeout was accepted")
	}
}

func TestLoadResponsesWebSocketOverrides(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_RESPONSES_WS_ENABLED", "false")
	t.Setenv("SUBPOOL_RESPONSES_WS_FORCE_HTTP_BRIDGE", "true")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ResponsesWSEnabled || !cfg.ResponsesWSForceHTTPBridge {
		t.Fatalf("WebSocket settings = %#v", cfg)
	}
}

func TestLoadRejectsInvalidResponsesWebSocketSetting(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_RESPONSES_WS_ENABLED", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("invalid WebSocket setting was accepted")
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
