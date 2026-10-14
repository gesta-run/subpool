// Package bailian reads Token Plan quota through the official console CLI gateway.
package bailian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"

	providerhttp "github.com/gesta-run/subpool/internal/provider/httpclient"
)

const PlanType = "bailian_token_plan"
const usageAPI = "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage"

var ErrAuthorizationRequired = errors.New("quota_authorization_required")
var ErrPermissionDenied = errors.New("quota_permission_denied")
var ErrInvalidResponse = errors.New("invalid_usage_response")

type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	AccessKeySecret string `json:"access_key_secret"`
}

type Window struct {
	UsedPercent      float64 `json:"used_percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	WindowSeconds    int64   `json:"window_seconds"`
	ResetAt          int64   `json:"reset_at"`
}

type Snapshot struct {
	PlanType string  `json:"plan_type"`
	FiveHour *Window `json:"five_hour,omitempty"`
	Weekly   *Window `json:"weekly,omitempty"`
}

type Client struct{ httpClient *http.Client }

func NewClient(client *http.Client) *Client {
	if client == nil {
		client = providerhttp.New()
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{httpClient: &copy}
}

func SupportsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "token-plan.cn-beijing.maas.aliyuncs.com" && strings.TrimRight(u.Path, "/") == "/compatible-mode/v1"
}

func (c *Client) Usage(ctx context.Context, credentials Credentials) (Snapshot, error) {
	if credentials.AccessKeyID == "" || credentials.AccessKeySecret == "" {
		return Snapshot{}, ErrAuthorizationRequired
	}
	token, err := c.accessToken(ctx, credentials)
	if err != nil {
		return Snapshot{}, err
	}
	params, _ := json.Marshal(map[string]any{"Api": usageAPI, "V": "1.0", "Data": map[string]any{
		"cornerstoneParam": map[string]any{"protocol": "V2", "console": "ONE_CONSOLE", "productCode": "p_efm", "switchUserType": 3, "consoleSite": "BAILIAN_ALIYUN"},
	}})
	form := url.Values{"params": {string(params)}, "region": {"cn-beijing"}}
	endpoint := "https://bailian-cs.console.aliyun.com/cli/api.json?action=BroadScopeAspnGateway&product=sfm_bailian&api=" + url.QueryEscape(usageAPI)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Snapshot{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+token)
	body, err := c.request(req)
	if err != nil {
		return Snapshot{}, err
	}
	return ParseUsage(body)
}

func (c *Client) request(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.New("quota_connection_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrPermissionDenied
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("quota_http_%d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return nil, ErrInvalidResponse
	}
	return body, nil
}

func ParseUsage(body []byte) (Snapshot, error) {
	for depth := 0; depth < 6; depth++ {
		var payload struct {
			Success       *bool           `json:"success"`
			ErrorCode     string          `json:"errorCode"`
			Data          json.RawMessage `json:"data"`
			DataV2        json.RawMessage `json:"DataV2"`
			FiveHour      *float64        `json:"per5HourPercentage"`
			Weekly        *float64        `json:"per1WeekPercentage"`
			FiveHourReset int64           `json:"per5HourResetTime"`
			WeeklyReset   int64           `json:"per1WeekResetTime"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return Snapshot{}, ErrInvalidResponse
		}
		if payload.Success != nil && !*payload.Success {
			if strings.Contains(payload.ErrorCode, "NotAuthorised") || strings.Contains(payload.ErrorCode, "NotLogined") {
				return Snapshot{}, ErrPermissionDenied
			}
			return Snapshot{}, ErrInvalidResponse
		}
		if payload.FiveHour != nil || payload.Weekly != nil {
			five, err := quotaWindow(payload.FiveHour, payload.FiveHourReset, 5*3600)
			if err != nil {
				return Snapshot{}, err
			}
			weekly, err := quotaWindow(payload.Weekly, payload.WeeklyReset, 7*24*3600)
			return Snapshot{PlanType: PlanType, FiveHour: five, Weekly: weekly}, err
		}
		body = payload.Data
		if len(payload.DataV2) > 0 {
			body = payload.DataV2
		}
	}
	return Snapshot{}, ErrInvalidResponse
}

func quotaWindow(fraction *float64, reset, seconds int64) (*Window, error) {
	if fraction == nil {
		return nil, nil
	}
	if math.IsNaN(*fraction) || math.IsInf(*fraction, 0) || *fraction < 0 || reset < 0 {
		return nil, ErrInvalidResponse
	}
	used := math.Min(100, *fraction*100)
	return &Window{UsedPercent: used, RemainingPercent: 100 - used, WindowSeconds: seconds, ResetAt: reset / 1000}, nil
}
