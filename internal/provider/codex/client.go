package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
)

const defaultUserAgent = "codex-tui/0.146.0 (subpool)"

type Client struct {
	baseURL    string
	usageURL   string
	httpClient *http.Client
}

type UsageWindow struct {
	UsedPercent      float64 `json:"used_percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	WindowSeconds    int64   `json:"window_seconds"`
	ResetAt          int64   `json:"reset_at"`
}

type UsageSnapshot struct {
	PlanType string       `json:"plan_type,omitempty"`
	FiveHour *UsageWindow `json:"five_hour,omitempty"`
	Weekly   *UsageWindow `json:"weekly,omitempty"`
}

type HTTPStatusError struct {
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("Codex usage endpoint returned status %d", e.StatusCode)
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = providerhttp.New()
	}
	baseURL = strings.TrimRight(baseURL, "/")
	usageURL := baseURL + "/usage"
	if strings.HasSuffix(baseURL, "/codex") {
		usageURL = strings.TrimSuffix(baseURL, "/codex") + "/wham/usage"
	}
	return &Client{baseURL: baseURL, usageURL: usageURL, httpClient: httpClient}
}

func (c *Client) Responses(ctx context.Context, body []byte, downstream http.Header, credentials Credentials) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Codex request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("User-Agent", defaultUserAgent)
	if credentials.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", credentials.AccountID)
	}
	for _, name := range []string{"Version", "X-Codex-Beta-Features", "X-Codex-Installation-Id", "X-Codex-Turn-Metadata", "X-Client-Request-Id", "X-Codex-Window-Id", "Thread-Id", "Session-Id", "X-Openai-Internal-Codex-Responses-Lite"} {
		if value := downstream.Get(name); value != "" {
			req.Header.Set(name, value)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send Codex request: %w", err)
	}
	return resp, nil
}

func (c *Client) Usage(ctx context.Context, credentials Credentials) (UsageSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.usageURL, nil)
	if err != nil {
		return UsageSnapshot{}, fmt.Errorf("create Codex usage request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultUserAgent)
	if credentials.AccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", credentials.AccountID)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return UsageSnapshot{}, fmt.Errorf("send Codex usage request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return UsageSnapshot{}, fmt.Errorf("read Codex usage response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return UsageSnapshot{}, &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	var payload struct {
		PlanType  string `json:"plan_type"`
		RateLimit *struct {
			Primary   *usageWindowResponse `json:"primary_window"`
			Secondary *usageWindowResponse `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return UsageSnapshot{}, fmt.Errorf("decode Codex usage response: %w", err)
	}
	if payload.RateLimit == nil {
		return UsageSnapshot{}, fmt.Errorf("Codex usage response is missing rate_limit")
	}
	primary := normalizeUsageWindow(payload.RateLimit.Primary)
	secondary := normalizeUsageWindow(payload.RateLimit.Secondary)
	snapshot := UsageSnapshot{PlanType: payload.PlanType, FiveHour: primary, Weekly: secondary}
	const weeklyWindowSeconds = 6 * 24 * 60 * 60
	if primary != nil && primary.WindowSeconds >= weeklyWindowSeconds {
		snapshot.Weekly = primary
		snapshot.FiveHour = secondary
	} else if secondary != nil && secondary.WindowSeconds < weeklyWindowSeconds {
		snapshot.Weekly = nil
	}
	return snapshot, nil
}

type usageWindowResponse struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowSeconds int64   `json:"limit_window_seconds"`
	ResetAt       int64   `json:"reset_at"`
}

func normalizeUsageWindow(window *usageWindowResponse) *UsageWindow {
	if window == nil {
		return nil
	}
	used := math.Max(0, math.Min(100, window.UsedPercent))
	return &UsageWindow{UsedPercent: used, RemainingPercent: 100 - used, WindowSeconds: window.WindowSeconds, ResetAt: window.ResetAt}
}
