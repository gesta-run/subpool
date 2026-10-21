package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/bailian"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type BailianUsage interface {
	Usage(context.Context, bailian.Credentials) (bailian.Snapshot, error)
}

func (c *Checker) checkCompatible(ctx context.Context, plaintext []byte) Result {
	var credentials openaicompat.Credentials
	if json.Unmarshal(plaintext, &credentials) != nil {
		return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "credential_unavailable", Failure: true}
	}
	probeCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	result := c.compatibleHealth(probeCtx, credentials)
	cancel()
	// The console quota API has separate credentials and availability from /models.
	quota := c.compatibleQuota(ctx, credentials)
	result.QuotaSnapshot = quota.QuotaSnapshot
	result.QuotaErrorCode = quota.QuotaErrorCode
	return result
}

func (c *Checker) compatibleHealth(ctx context.Context, credentials openaicompat.Credentials) Result {
	resp, err := c.compatible.Models(ctx, credentials)
	if err != nil {
		return classifyError(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return Result{HealthStatus: domain.HealthHealthy}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "authentication_failed", AuthFailed: true}
	case resp.StatusCode == http.StatusTooManyRequests:
		return Result{HealthStatus: domain.HealthHealthy}
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "probe_unsupported"}
	case resp.StatusCode >= 500:
		return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_5xx", Failure: true}
	default:
		return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_error", Failure: true}
	}
}

func (c *Checker) compatibleQuota(ctx context.Context, credentials openaicompat.Credentials) Result {
	result := Result{HealthStatus: domain.HealthHealthy}
	if !bailian.SupportsURL(credentials.BaseURL) {
		return result
	}
	if credentials.BailianQuota == nil {
		result.QuotaErrorCode = "quota_authorization_required"
		return result
	}
	snapshot, err := c.bailian.Usage(ctx, *credentials.BailianQuota)
	if err != nil {
		result.QuotaErrorCode = "quota_unavailable"
		if errors.Is(err, bailian.ErrAuthorizationExpired) {
			result.QuotaErrorCode = "quota_authorization_expired"
		}
		if errors.Is(err, bailian.ErrPermissionDenied) {
			result.QuotaErrorCode = "quota_permission_denied"
		}
		if errors.Is(err, bailian.ErrInvalidResponse) {
			result.QuotaErrorCode = "invalid_usage_response"
		}
		return result
	}
	result.QuotaSnapshot, _ = json.Marshal(snapshot)
	// Quota is informational: its query permission is independent of inference.
	return result
}
