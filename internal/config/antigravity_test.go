package config

import (
	"strings"
	"testing"
)

func TestLoadAntigravityWithoutOAuthConfiguration(t *testing.T) {
	setValidEnv(t)
	t.Setenv("SUBPOOL_ANTIGRAVITY_BASE_URL", "")
	t.Setenv("SUBPOOL_ANTIGRAVITY_USER_AGENT", "")
	t.Setenv("SUBPOOL_ANTIGRAVITY_CLIENT_ID", "")
	t.Setenv("SUBPOOL_ANTIGRAVITY_CLIENT_SECRET", "")
	t.Setenv("SUBPOOL_ANTIGRAVITY_REDIRECT_URI", "not-a-valid-callback")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AntigravityClientID != "" || cfg.AntigravityClientSecret != "" || cfg.AntigravityBaseURL != "https://cloudcode-pa.googleapis.com" || cfg.AntigravityUserAgent == "" {
		t.Fatal("default Antigravity upstream configuration is missing")
	}
}

func TestLoadAntigravityOAuthEnvironment(t *testing.T) {
	for _, test := range []struct {
		name, clientID, secret string
		wantError              bool
	}{
		{name: "configured", clientID: "example-client", secret: "example-secret"},
		{name: "trimmed", clientID: " example-client ", secret: " example-secret "},
		{name: "empty defaults"},
		{name: "blank defaults", clientID: " ", secret: " "},
		{name: "missing secret", clientID: "example-client", wantError: true},
		{name: "missing client ID", secret: "example-secret", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			setValidEnv(t)
			t.Setenv("SUBPOOL_ANTIGRAVITY_CLIENT_ID", test.clientID)
			t.Setenv("SUBPOOL_ANTIGRAVITY_CLIENT_SECRET", test.secret)
			cfg, err := Load()
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "configure both SUBPOOL_ANTIGRAVITY_CLIENT_ID and SUBPOOL_ANTIGRAVITY_CLIENT_SECRET") {
					t.Fatal("incomplete OAuth client configuration was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.AntigravityClientID != strings.TrimSpace(test.clientID) || cfg.AntigravityClientSecret != strings.TrimSpace(test.secret) {
				t.Fatal("OAuth client configuration did not come from the environment")
			}
		})
	}
}
