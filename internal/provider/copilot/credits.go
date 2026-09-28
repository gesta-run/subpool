package copilot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

type rawCreditsQuota struct {
	CreditsUsed      *float64 `json:"credits_used"`
	Entitlement      *float64 `json:"entitlement"`
	Remaining        *float64 `json:"remaining"`
	PercentRemaining *float64 `json:"percent_remaining"`
	Unlimited        bool     `json:"unlimited"`
	OveragePermitted bool     `json:"overage_permitted"`
	OverageCount     float64  `json:"overage_count"`
}

type rawCreditsSnapshot struct {
	CopilotPlan          string                     `json:"copilot_plan"`
	QuotaSnapshots       map[string]rawCreditsQuota `json:"quota_snapshots"`
	QuotaResetDateUTC    string                     `json:"quota_reset_date_utc"`
	QuotaResetDate       string                     `json:"quota_reset_date"`
	LimitedUserResetDate string                     `json:"limited_user_reset_date"`
}

func (c *Client) Credits(ctx context.Context, credentials Credentials) (CreditsSnapshot, error) {
	if strings.TrimSpace(credentials.GitHubToken) == "" {
		return CreditsSnapshot{}, ErrCredentialsIncomplete
	}
	if _, err := c.token(ctx, credentials); err != nil {
		return CreditsSnapshot{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.entitlementsURL, nil)
	if err != nil {
		return CreditsSnapshot{}, fmt.Errorf("create Copilot credits request: %w", err)
	}
	req.Header.Set("Authorization", "token "+credentials.GitHubToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	setEditorHeaders(req.Header)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return CreditsSnapshot{}, fmt.Errorf("read Copilot credits: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return CreditsSnapshot{}, &HTTPError{StatusCode: resp.StatusCode, Operation: "Copilot AI credits"}
	}
	var upstream rawCreditsSnapshot
	if err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&upstream); err != nil {
		return CreditsSnapshot{}, fmt.Errorf("decode Copilot credits: %w", err)
	}
	return normalizeCreditsSnapshot(upstream), nil
}

func normalizeCreditsSnapshot(upstream rawCreditsSnapshot) CreditsSnapshot {
	result := CreditsSnapshot{PlanType: upstream.CopilotPlan}
	raw, ok := preferredCreditsQuota(upstream.QuotaSnapshots)
	if !ok {
		return result
	}
	credits, allowed, ok := normalizeCreditsQuota(raw, quotaResetAt(upstream))
	if !ok {
		return result
	}
	result.Credits = &credits
	result.UsageAllowed = &allowed
	return result
}

func preferredCreditsQuota(quotas map[string]rawCreditsQuota) (rawCreditsQuota, bool) {
	// GitHub retained the premium_interactions key when Copilot billing moved
	// to AI credits, so prefer an explicit future key but keep both legacy keys.
	for _, name := range []string{"ai_credits", "premium_interactions", "premium_models"} {
		if quota, ok := quotas[name]; ok {
			return quota, true
		}
	}
	return rawCreditsQuota{}, false
}

func normalizeCreditsQuota(raw rawCreditsQuota, resetAt int64) (CreditsQuota, bool, bool) {
	if raw.Entitlement == nil || !finite(*raw.Entitlement) {
		return CreditsQuota{}, false, false
	}
	entitlement := *raw.Entitlement
	unlimited := raw.Unlimited || entitlement < 0
	if unlimited {
		return CreditsQuota{
			Entitlement: entitlement, RemainingPercent: 100, Unlimited: true,
			OveragePermitted: raw.OveragePermitted, OverageCount: nonNegative(raw.OverageCount), ResetAt: resetAt,
		}, true, true
	}
	entitlement = nonNegative(entitlement)
	remaining, ok := absoluteRemaining(raw, entitlement)
	if !ok {
		return CreditsQuota{}, false, false
	}
	remaining = clamp(remaining, 0, entitlement)
	used := entitlement - remaining
	if raw.CreditsUsed != nil && finite(*raw.CreditsUsed) {
		used = clamp(*raw.CreditsUsed, 0, entitlement)
	}
	remainingPercent := 0.0
	if entitlement > 0 {
		remainingPercent = remaining / entitlement * 100
	}
	if raw.PercentRemaining != nil && finite(*raw.PercentRemaining) {
		remainingPercent = clamp(*raw.PercentRemaining, 0, 100)
	}
	allowed := remaining > 0 || raw.OveragePermitted
	return CreditsQuota{
		Used: used, Entitlement: entitlement, Remaining: remaining, RemainingPercent: remainingPercent,
		OveragePermitted: raw.OveragePermitted, OverageCount: nonNegative(raw.OverageCount), ResetAt: resetAt,
	}, allowed, true
}

func absoluteRemaining(raw rawCreditsQuota, entitlement float64) (float64, bool) {
	if raw.Remaining != nil && finite(*raw.Remaining) {
		return *raw.Remaining, true
	}
	if raw.CreditsUsed != nil && finite(*raw.CreditsUsed) {
		return entitlement - *raw.CreditsUsed, true
	}
	if raw.PercentRemaining != nil && finite(*raw.PercentRemaining) {
		return entitlement * clamp(*raw.PercentRemaining, 0, 100) / 100, true
	}
	return 0, false
}

func quotaResetAt(snapshot rawCreditsSnapshot) int64 {
	for _, value := range []string{snapshot.QuotaResetDateUTC, snapshot.QuotaResetDate, snapshot.LimitedUserResetDate} {
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed.Unix()
			}
		}
	}
	return 0
}

func nonNegative(value float64) float64 {
	if !finite(value) || value < 0 {
		return 0
	}
	return value
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
