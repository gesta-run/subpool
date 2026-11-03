package openaibilling

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
)

// estimatedUsage reads organization-wide USD costs for one configured grant.
// It does not discover grants or change model access or billing controls.
func (c *Client) estimatedUsage(ctx context.Context, credentials Credentials) (Snapshot, error) {
	now := c.now().Unix()
	if credentials.GrantStartAt >= now {
		return Snapshot{}, ErrInvalidResponse
	}
	end := now
	if credentials.GrantExpiresAt < end {
		end = credentials.GrantExpiresAt
	}
	query := url.Values{"start_time": {strconv.FormatInt(credentials.GrantStartAt, 10)}, "end_time": {strconv.FormatInt(end, 10)}, "bucket_width": {"1d"}, "limit": {"180"}}
	used := 0.0
	through := int64(0)
	cursors := map[string]bool{}
	for {
		resp, err := c.get(ctx, "organization/costs?"+query.Encode(), credentials.AdminKey, credentials.OrganizationID)
		if err != nil {
			return Snapshot{}, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				return Snapshot{}, ErrAuthorizationExpired
			}
			return Snapshot{}, ErrUnavailable
		}
		var page struct {
			Data *[]struct {
				End     int64 `json:"end_time"`
				Results []struct {
					Object string `json:"object"`
					Amount *struct {
						Value    *float64 `json:"value"`
						Currency string   `json:"currency"`
					} `json:"amount"`
				} `json:"results"`
			} `json:"data"`
			HasMore  bool   `json:"has_more"`
			NextPage string `json:"next_page"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil || page.Data == nil {
			return Snapshot{}, ErrInvalidResponse
		}
		for _, bucket := range *page.Data {
			if len(bucket.Results) > 0 && bucket.End > through {
				through = bucket.End
			}
			for _, result := range bucket.Results {
				if result.Object != "organization.costs.result" || result.Amount == nil || result.Amount.Value == nil || result.Amount.Currency != "usd" {
					return Snapshot{}, ErrInvalidResponse
				}
				used += *result.Amount.Value
			}
		}
		if !page.HasMore {
			break
		}
		if page.NextPage == "" || cursors[page.NextPage] {
			return Snapshot{}, ErrInvalidResponse
		}
		cursors[page.NextPage] = true
		query.Set("page", page.NextPage)
	}
	if math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > 1e15 {
		return Snapshot{}, ErrInvalidResponse
	}
	if through > end {
		through = end
	}
	remaining := math.Max(0, credentials.GrantAmount-used)
	if now >= credentials.GrantExpiresAt {
		remaining = 0
	}
	return Snapshot{PlanType: PlanType, Balance: Balance{Granted: credentials.GrantAmount, Used: used, Remaining: remaining, ExpiresAt: credentials.GrantExpiresAt, Estimated: true, UsageThrough: through, GrantStartAt: credentials.GrantStartAt}}, nil
}
