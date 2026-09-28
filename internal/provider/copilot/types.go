package copilot

import "time"

const (
	DefaultAPIBase          = "https://api.githubcopilot.com"
	DefaultTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"
	DefaultDeviceCodeURL    = "https://github.com/login/device/code"
	DefaultAccessTokenURL   = "https://github.com/login/oauth/access_token"
	DefaultUserURL          = "https://api.github.com/user"
	DefaultOAuthClientID    = "Iv1.b507a08c87ecfe98"
)

type Credentials struct {
	GitHubToken string `json:"github_token"`
	Login       string `json:"login"`
	UserID      int64  `json:"user_id"`
	Email       string `json:"email,omitempty"`
}

type DeviceAuthorization struct {
	LoginID         string    `json:"login_id"`
	UserCode        string    `json:"user_code"`
	VerificationURL string    `json:"verification_url"`
	ExpiresAt       time.Time `json:"expires_at"`
}

type DeviceAuthorizationResult struct {
	Credentials Credentials
	Err         error
}

type Model struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Preview     bool   `json:"preview"`
	Disabled    bool   `json:"disabled"`
	Hidden      bool   `json:"hidden"`
}

type accessToken struct {
	value     string
	apiBase   string
	expiresAt time.Time
	refreshAt time.Time
}
