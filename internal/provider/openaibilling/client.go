// Package openaibilling reads the API Platform's browser-authenticated balance.
// This dashboard endpoint is not a public API; expired sessions require reconnecting.
package openaibilling

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const PlanType = "openai_api_credits"

var (
	ErrAuthorizationExpired = errors.New("billing authorization expired or rejected")
	ErrInvalidResponse      = errors.New("invalid billing response")
	ErrUnavailable          = errors.New("billing service unavailable")
	ErrOrganizationMismatch = errors.New("billing organization does not match inference credentials")
)

type Credentials struct {
	SessionKey     string `json:"session_key"`
	OrganizationID string `json:"organization_id"`
}

func (c Credentials) Valid() bool {
	return c.SessionKey != "" && len(c.SessionKey) <= 8192 && !strings.ContainsAny(c.SessionKey, "\r\n") && strings.HasPrefix(c.OrganizationID, "org-") && !strings.ContainsAny(c.OrganizationID, "\r\n")
}

func SupportsURL(base string) bool {
	return strings.TrimRight(strings.TrimSpace(base), "/") == "https://api.openai.com/v1"
}

type Balance struct {
	Granted       float64 `json:"granted_usd"`
	Used          float64 `json:"used_usd"`
	Remaining     float64 `json:"remaining_usd"`
	PaidAvailable float64 `json:"paid_available_usd"`
	ExpiresAt     int64   `json:"expires_at,omitempty"`
}
type Snapshot struct {
	PlanType string  `json:"plan_type"`
	Balance  Balance `json:"balance"`
}

type Client struct {
	http *http.Client
	now  func() time.Time
}

func NewClient(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	copy := *client
	// Never forward a browser session to a redirect destination.
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: &copy, now: time.Now}
}

func (c *Client) get(ctx context.Context, path, key, org string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/"+path, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("OpenAI-Organization", org)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	return resp, nil
}

// VerifyAccount prevents attaching another organization's balance to this key.
func (c *Client) VerifyAccount(ctx context.Context, apiKey, org string) error {
	resp, err := c.get(ctx, "models", apiKey, org)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return ErrOrganizationMismatch
	}
	if resp.StatusCode != http.StatusOK {
		return ErrUnavailable
	}
	return nil
}

func (c *Client) Usage(ctx context.Context, credentials Credentials) (Snapshot, error) {
	if !credentials.Valid() {
		return Snapshot{}, ErrAuthorizationExpired
	}
	resp, err := c.get(ctx, "dashboard/billing/credit_grants", credentials.SessionKey, credentials.OrganizationID)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return Snapshot{}, ErrAuthorizationExpired
	}
	if resp.StatusCode != http.StatusOK {
		return Snapshot{}, ErrUnavailable
	}
	var data struct {
		Granted   *float64 `json:"total_granted"`
		Used      *float64 `json:"total_used"`
		Remaining *float64 `json:"total_available"`
		Paid      float64  `json:"total_paid_available"`
		Grants    struct {
			Data []struct {
				Amount    float64 `json:"grant_amount"`
				Used      float64 `json:"used_amount"`
				Expires   float64 `json:"expires_at"`
				Scheduled bool    `json:"is_scheduled"`
			} `json:"data"`
		} `json:"grants"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&data) != nil || data.Granted == nil || data.Used == nil || data.Remaining == nil {
		return Snapshot{}, ErrInvalidResponse
	}
	for _, v := range []float64{*data.Granted, *data.Used, *data.Remaining, data.Paid} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e15 {
			return Snapshot{}, ErrInvalidResponse
		}
	}
	if *data.Granted < 0 || *data.Used < 0 || data.Paid < 0 {
		return Snapshot{}, ErrInvalidResponse
	}
	balance := Balance{Granted: *data.Granted, Used: *data.Used, Remaining: *data.Remaining, PaidAvailable: data.Paid}
	for _, grant := range data.Grants.Data {
		if grant.Scheduled || grant.Amount <= grant.Used || grant.Expires <= float64(c.now().Unix()) || grant.Expires > 253402300799 {
			continue
		}
		expires := int64(grant.Expires)
		if balance.ExpiresAt == 0 || expires < balance.ExpiresAt {
			balance.ExpiresAt = expires
		}
	}
	return Snapshot{PlanType: PlanType, Balance: balance}, nil
}
