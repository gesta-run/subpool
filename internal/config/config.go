package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddress     string
	DatabaseURL       string
	PublicURL         string
	AdminUsername     string
	AdminPassword     string
	CredentialKey     []byte
	APIKeyHMACKey     []byte
	SessionTTL        time.Duration
	CodexClientID     string
	CodexTokenURL     string
	CodexUpstreamURL  string
	TrustedProxyCIDRs []string
}

func Load() (Config, error) {
	cfg := Config{
		ListenAddress:    envOr("SUBPOOL_LISTEN_ADDRESS", ":8080"),
		DatabaseURL:      strings.TrimSpace(os.Getenv("SUBPOOL_DATABASE_URL")),
		PublicURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("SUBPOOL_PUBLIC_URL")), "/"),
		AdminUsername:    strings.TrimSpace(os.Getenv("SUBPOOL_ADMIN_USERNAME")),
		AdminPassword:    os.Getenv("SUBPOOL_ADMIN_PASSWORD"),
		SessionTTL:       12 * time.Hour,
		CodexClientID:    envOr("SUBPOOL_CODEX_CLIENT_ID", "app_EMoamEEZ73f0CkXaXp7hrann"),
		CodexTokenURL:    envOr("SUBPOOL_CODEX_TOKEN_URL", "https://auth.openai.com/oauth/token"),
		CodexUpstreamURL: strings.TrimRight(envOr("SUBPOOL_CODEX_UPSTREAM_URL", "https://chatgpt.com/backend-api/codex"), "/"),
	}

	var err error
	if cfg.CredentialKey, err = decodeKey("SUBPOOL_CREDENTIAL_KEY", 32); err != nil {
		return Config{}, err
	}
	if cfg.APIKeyHMACKey, err = decodeKey("SUBPOOL_API_KEY_HMAC_KEY", 32); err != nil {
		return Config{}, err
	}
	if raw := strings.TrimSpace(os.Getenv("SUBPOOL_SESSION_TTL")); raw != "" {
		cfg.SessionTTL, err = time.ParseDuration(raw)
		if err != nil || cfg.SessionTTL <= 0 {
			return Config{}, fmt.Errorf("SUBPOOL_SESSION_TTL must be a positive duration")
		}
	}

	switch {
	case cfg.DatabaseURL == "":
		return Config{}, errors.New("SUBPOOL_DATABASE_URL is required")
	case cfg.PublicURL == "":
		return Config{}, errors.New("SUBPOOL_PUBLIC_URL is required")
	case cfg.AdminUsername == "":
		return Config{}, errors.New("SUBPOOL_ADMIN_USERNAME is required")
	case cfg.AdminPassword == "":
		return Config{}, errors.New("SUBPOOL_ADMIN_PASSWORD is required")
	}
	parsedURL, parseErr := url.ParseRequestURI(cfg.PublicURL)
	if parseErr != nil || parsedURL.Scheme == "" || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		err = parseErr
		if err == nil {
			err = errors.New("URL must use http or https and include a host")
		}
		return Config{}, fmt.Errorf("SUBPOOL_PUBLIC_URL is invalid: %w", err)
	}
	if (parsedURL.Path != "" && parsedURL.Path != "/") || parsedURL.RawQuery != "" || parsedURL.Fragment != "" || parsedURL.User != nil {
		return Config{}, errors.New("SUBPOOL_PUBLIC_URL must be an origin without credentials, path, query, or fragment")
	}
	if raw := strings.TrimSpace(os.Getenv("SUBPOOL_TRUSTED_PROXY_CIDRS")); raw != "" {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, _, parseErr := net.ParseCIDR(value); parseErr != nil {
				return Config{}, fmt.Errorf("SUBPOOL_TRUSTED_PROXY_CIDRS contains invalid CIDR %q", value)
			}
			cfg.TrustedProxyCIDRs = append(cfg.TrustedProxyCIDRs, value)
		}
	}
	return cfg, nil
}

func decodeKey(name string, expected int) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) != expected {
		return nil, fmt.Errorf("%s must be base64-encoded %d bytes", name, expected)
	}
	return decoded, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
