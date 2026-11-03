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

func (c *Checker) openAIQuota(ctx context.Context, credentials openaicompat.Credentials) Result {
	result := Result{HealthStatus: domain.HealthHealthy}
	if credentials.OpenAIBilling == nil {
		result.QuotaErrorCode = "quota_authorization_required"
		return result
	}
	client := c.openaiBalance
	if client == nil {
		client = openaibilling.NewClient(nil)
	}
	snapshot, err := client.Usage(ctx, *credentials.OpenAIBilling)
	if err != nil {
		// Billing errors never include credential headers or upstream response bodies.
		slog.Warn("OpenAI balance read failed", "error", err)
		result.QuotaErrorCode = "quota_unavailable"
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
