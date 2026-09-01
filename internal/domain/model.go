package domain

import (
	"encoding/json"
	"time"
)

const (
	ProviderCodex          = "codex"
	CredentialSubscription = "subscription_oauth"
	StrategyLeastAssigned  = "least_assigned"
	AccountActive          = "active"
	AccountCoolingDown     = "cooling_down"
	AccountExhausted       = "exhausted"
	AccountAuthFailed      = "auth_failed"
	AccountDisabled        = "disabled"
)

type ProviderAccount struct {
	ID                   string          `json:"id"`
	Provider             string          `json:"provider"`
	CredentialType       string          `json:"credential_type"`
	DisplayName          string          `json:"display_name"`
	SubjectHMAC          []byte          `json:"-"`
	CredentialCiphertext []byte          `json:"-"`
	CredentialVersion    int             `json:"credential_version"`
	Status               string          `json:"status"`
	MaxAPIKeys           int             `json:"max_api_keys"`
	AssignedAPIKeys      int             `json:"assigned_api_keys"`
	QuotaSnapshot        json.RawMessage `json:"quota_snapshot"`
	CooldownUntil        *time.Time      `json:"cooldown_until,omitempty"`
	LastSuccessAt        *time.Time      `json:"last_success_at,omitempty"`
	LastFailureAt        *time.Time      `json:"last_failure_at,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
}

type Settings struct {
	MaxAPIKeysPerAccount int       `json:"max_api_keys_per_account"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type Pool struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Provider       string        `json:"provider"`
	Strategy       string        `json:"strategy"`
	ModelAllowlist []string      `json:"model_allowlist"`
	Accounts       []PoolAccount `json:"accounts,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

type PoolAccount struct {
	PoolID            string `json:"pool_id"`
	ProviderAccountID string `json:"provider_account_id"`
	Weight            int    `json:"weight"`
	Priority          int    `json:"priority"`
	Enabled           bool   `json:"enabled"`
}

type APIKey struct {
	ID                string     `json:"id"`
	PoolID            string     `json:"pool_id"`
	ProviderAccountID string     `json:"provider_account_id"`
	EmployeeName      string     `json:"employee_name"`
	KeyHMAC           []byte     `json:"-"`
	KeyHint           string     `json:"key_hint"`
	Scopes            []string   `json:"scopes"`
	RateLimit         int        `json:"rate_limit"`
	ExpiresAt         *time.Time `json:"expires_at,omitempty"`
	RevokedAt         *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
}

type KeyRoute struct {
	Key               APIKey
	Pool              Pool
	Account           ProviderAccount
	MembershipEnabled bool
}

type UsageRow struct {
	APIKeyID     string    `json:"api_key_id"`
	EmployeeName string    `json:"employee_name"`
	UsageDate    time.Time `json:"usage_date"`
	InputTokens  int64     `json:"input_tokens"`
	OutputTokens int64     `json:"output_tokens"`
}

type UsageFilter struct {
	APIKeyID string
	From     *time.Time
	To       *time.Time
}

type AuditEvent struct {
	Actor      string
	Action     string
	TargetType string
	TargetID   string
	Result     string
}
