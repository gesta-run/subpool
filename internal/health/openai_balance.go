package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type OpenAIBalance interface {
	Usage(context.Context, openaibilling.Credentials) (openaibilling.Snapshot, error)
}

func (c *Checker) openAIQuota(ctx context.Context, credentials openaicompat.Credentials, account domain.ProviderAccount, force bool) Result {
	result := Result{HealthStatus: domain.HealthHealthy}
	if credentials.OpenAIBilling == nil {
		result.QuotaErrorCode = "quota_authorization_required"
		return result
	}
	if credentials.OpenAIBilling.AdminKey != "" && !c.claimBillingPoll(account, *credentials.OpenAIBilling, force) {
		result.QuotaSkipped = true
		return result
	}
	client := c.openaiBalance
	if client == nil {
		client = openaibilling.NewClient(nil)
	}
	snapshot, err := client.Usage(ctx, *credentials.OpenAIBilling)
	if credentials.OpenAIBilling.AdminKey != "" {
		c.finishBillingPoll(account.ID, *credentials.OpenAIBilling, err)
	}
	if err != nil {
		// Billing errors never include credential headers or upstream response bodies.
		slog.Warn("OpenAI balance read failed", "error", err)
		result.QuotaErrorCode = "quota_unavailable"
		var limited *openaibilling.RateLimitError
		if errors.As(err, &limited) && (limited.Code == "rate_limit_exceeded" || limited.Code == "slow_down" || limited.Code == "unknown") {
			result.QuotaErrorCode = "quota_probe_rate_limited"
		}
		if errors.Is(err, openaibilling.ErrAuthorizationExpired) {
			result.QuotaErrorCode = "quota_authorization_expired"
		}
		if errors.Is(err, openaibilling.ErrInvalidResponse) {
			result.QuotaErrorCode = "invalid_usage_response"
		}
		return result
	}
	result.QuotaSnapshot, _ = json.Marshal(snapshot)
	return result
}
