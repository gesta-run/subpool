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
		defer c.finishBillingPoll(account.ID, *credentials.OpenAIBilling, err)
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
	if snapshot.Balance.Estimated {
		browser := credentials.OpenAIBilling.GrantCredentials()
		var previous openaibilling.Snapshot
		if json.Unmarshal(account.QuotaSnapshot, &previous) == nil && previous.PlanType == openaibilling.PlanType &&
			previous.Balance.GrantStartAt == snapshot.Balance.GrantStartAt &&
			previous.Balance.ExpiresAt == snapshot.Balance.ExpiresAt &&
			previous.Balance.Granted == snapshot.Balance.Granted {
			snapshot.GrantDepletedAt = previous.GrantDepletedAt
		}
		if account.Status == domain.AccountExhausted && snapshot.Balance.Remaining > 0 &&
			snapshot.GrantDepletedAt == 0 && browser.Valid() {
			grant, grantErr := client.Usage(ctx, browser)
			if grantErr == nil && grant.PlanType == openaibilling.PlanType && grant.Balance.Remaining <= 0 && grant.Balance.PaidAvailable == 0 {
				snapshot.GrantDepletedAt = c.now().Unix()
			}
		}
	}
	result.QuotaSnapshot, _ = json.Marshal(snapshot)
	return result
}
