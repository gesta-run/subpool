package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/bailian"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type compatibleProbe func(context.Context, openaicompat.Credentials) (*http.Response, error)

func (f compatibleProbe) Models(ctx context.Context, credentials openaicompat.Credentials) (*http.Response, error) {
	return f(ctx, credentials)
}

type quotaProbe func(context.Context, bailian.Credentials) (bailian.Snapshot, error)

func (f quotaProbe) Usage(ctx context.Context, credentials bailian.Credentials) (bailian.Snapshot, error) {
	return f(ctx, credentials)
}

func bailianTestCredentials(t *testing.T) []byte {
	t.Helper()
	plaintext, err := json.Marshal(openaicompat.Credentials{
		BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
		APIKey:  "illustrative-key",
		BailianQuota: &bailian.Credentials{
			AccessKeyID: "illustrative-id", AccessKeySecret: "illustrative-secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func TestBailianQuotaRefreshSurvivesModelProbeFailure(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		err     error
		health  string
		code    string
		failure bool
		auth    bool
	}{
		{name: "rate limited", status: 429, health: domain.HealthHealthy},
		{name: "unsupported", status: 404, health: domain.HealthUnknown, code: "probe_unsupported"},
		{name: "method unsupported", status: 405, health: domain.HealthUnknown, code: "probe_unsupported"},
		{name: "provider unavailable", status: 503, health: domain.HealthUnknown, code: "provider_5xx", failure: true},
		{name: "model key revoked", status: 401, health: domain.HealthUnhealthy, code: "authentication_failed", auth: true},
		{name: "model key forbidden", status: 403, health: domain.HealthUnhealthy, code: "authentication_failed", auth: true},
		{name: "connection failure", err: errors.New("connection reset"), health: domain.HealthUnknown, code: "connection_failed", failure: true},
		{name: "timeout", err: context.DeadlineExceeded, health: domain.HealthUnknown, code: "timeout", failure: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			probe := compatibleProbe(func(context.Context, openaicompat.Credentials) (*http.Response, error) {
				if tt.err != nil {
					return nil, tt.err
				}
				return &http.Response{StatusCode: tt.status, Body: io.NopCloser(strings.NewReader("{}"))}, nil
			})
			checker := NewChecker(nil, testCipher{plaintext: bailianTestCredentials(t)}, nil, probe)
			usage := &testBailianUsage{}
			checker.bailian = usage
			result := checker.Check(context.Background(), domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible})
			if usage.calls != 1 || len(result.QuotaSnapshot) == 0 || result.QuotaErrorCode != "" {
				t.Fatalf("independent quota was not refreshed: calls=%d result=%+v", usage.calls, result)
			}
			if result.HealthStatus != tt.health || result.ErrorCode != tt.code || result.Failure != tt.failure || result.AuthFailed != tt.auth {
				t.Fatalf("quota refresh changed model health: %+v", result)
			}
		})
	}
}

func TestBailianQuotaHasSeparateDeadlineBudget(t *testing.T) {
	var modelDeadline time.Time
	probe := compatibleProbe(func(ctx context.Context, _ openaicompat.Credentials) (*http.Response, error) {
		var ok bool
		modelDeadline, ok = ctx.Deadline()
		if !ok || time.Until(modelDeadline) > checkTimeout {
			t.Fatal("model probe must retain its short deadline")
		}
		return nil, context.DeadlineExceeded
	})
	checker := NewChecker(nil, testCipher{plaintext: bailianTestCredentials(t)}, nil, probe)
	called := false
	checker.bailian = quotaProbe(func(ctx context.Context, _ bailian.Credentials) (bailian.Snapshot, error) {
		called = true
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Sub(modelDeadline) < CheckTimeout-checkTimeout-time.Second || ctx.Err() != nil {
			t.Fatalf("quota request inherited the model probe deadline: deadline=%v err=%v", deadline, ctx.Err())
		}
		return bailian.Snapshot{PlanType: bailian.PlanType}, nil
	})
	checker.Check(context.Background(), domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible})
	if !called {
		t.Fatal("quota request was skipped")
	}
}

func TestBailianQuotaRespectsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := compatibleProbe(func(ctx context.Context, _ openaicompat.Credentials) (*http.Response, error) {
		return nil, ctx.Err()
	})
	checker := NewChecker(nil, testCipher{plaintext: bailianTestCredentials(t)}, nil, probe)
	checker.bailian = quotaProbe(func(ctx context.Context, _ bailian.Credentials) (bailian.Snapshot, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("quota request escaped caller cancellation")
		}
		return bailian.Snapshot{}, ctx.Err()
	})
	result := checker.Check(ctx, domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible})
	if result.QuotaErrorCode != "quota_unavailable" || len(result.QuotaSnapshot) != 0 {
		t.Fatalf("canceled quota request returned a snapshot: %+v", result)
	}
}
