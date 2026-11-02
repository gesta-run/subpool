package health

import (
	"context"
	"encoding/json"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"io"
	"net/http"
	"strings"
	"testing"
)

type balanceProbe struct{ err error }

func (p balanceProbe) Usage(context.Context, openaibilling.Credentials) (openaibilling.Snapshot, error) {
	return openaibilling.Snapshot{PlanType: openaibilling.PlanType, Balance: openaibilling.Balance{Remaining: 25}}, p.err
}
func TestBillingAuthorizationDoesNotDisableInference(t *testing.T) {
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.openai.com/v1", APIKey: "model-key", OpenAIBilling: &openaibilling.Credentials{SessionKey: "session-key", OrganizationID: "org-example"}})
	checker := NewChecker(nil, testCipher{plaintext: raw}, nil, compatibleProbe(func(_ context.Context, c openaicompat.Credentials) (*http.Response, error) {
		if c.APIKey != "model-key" {
			t.Fatal("inference credential changed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}))
	checker.openaiBalance = balanceProbe{err: openaibilling.ErrAuthorizationExpired}
	got := checker.Check(context.Background(), domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible})
	if got.AuthFailed || got.Failure || got.HealthStatus != domain.HealthHealthy || got.QuotaErrorCode != "quota_authorization_expired" || got.UsageAllowed != nil {
		t.Fatalf("billing failure affected model routing: %+v", got)
	}
	checker.openaiBalance = balanceProbe{}
	if got = checker.Check(context.Background(), domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible}); len(got.QuotaSnapshot) == 0 || got.QuotaErrorCode != "" {
		t.Fatal("valid balance not returned")
	}
}
