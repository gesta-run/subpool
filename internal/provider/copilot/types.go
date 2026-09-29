package copilot

import (
	"errors"
	"strings"
	"time"
)

var ErrCredentialsIncomplete = errors.New("Copilot credentials are incomplete")

const (
	DefaultAPIBase          = "https://api.githubcopilot.com"
	DefaultTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"
	DefaultEntitlementsURL  = "https://api.github.com/copilot_internal/user"
	DefaultDeviceCodeURL    = "https://github.com/login/device/code"
	DefaultAccessTokenURL   = "https://github.com/login/oauth/access_token"
	DefaultUserURL          = "https://api.github.com/user"
	DefaultOAuthClientID    = "Iv1.b507a08c87ecfe98"
	EndpointChatCompletions = "/chat/completions"
	EndpointResponses       = "/responses"
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
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	DisplayName        string   `json:"display_name"`
	Preview            bool     `json:"preview"`
	Disabled           bool     `json:"disabled"`
	Hidden             bool     `json:"hidden"`
	SupportedEndpoints []string `json:"supported_endpoints"`
}

func (m Model) Identifier() string {
	if id := strings.TrimSpace(m.ID); id != "" {
		return id
	}
	return strings.TrimSpace(m.Name)
}

func (m Model) SupportsEndpoint(endpoint string) bool {
	if len(m.SupportedEndpoints) == 0 {
		return endpoint == EndpointChatCompletions
	}
	for _, supported := range m.SupportedEndpoints {
		if normalizeEndpoint(supported) == normalizeEndpoint(endpoint) {
			return true
		}
	}
	return false
}

func (m Model) SupportsGatewayEndpoint() bool {
	return m.SupportsEndpoint(EndpointChatCompletions) || m.SupportsEndpoint(EndpointResponses)
}

func normalizeEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(strings.ToLower(endpoint))
	if strings.HasPrefix(endpoint, "/v1/") {
		return strings.TrimPrefix(endpoint, "/v1")
	}
	return endpoint
}

type CreditsQuota struct {
	Used             float64 `json:"used"`
	Entitlement      float64 `json:"entitlement"`
	Remaining        float64 `json:"remaining"`
	RemainingPercent float64 `json:"remaining_percent"`
	Unlimited        bool    `json:"unlimited"`
	OveragePermitted bool    `json:"overage_permitted"`
	OverageCount     float64 `json:"overage_count"`
	ResetAt          int64   `json:"reset_at,omitempty"`
}

type CreditsSnapshot struct {
	PlanType     string        `json:"plan_type,omitempty"`
	UsageAllowed *bool         `json:"usage_allowed,omitempty"`
	Credits      *CreditsQuota `json:"credits,omitempty"`
}

type accessToken struct {
	value     string
	apiBase   string
	expiresAt time.Time
	refreshAt time.Time
}
