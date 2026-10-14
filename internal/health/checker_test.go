package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/bailian"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/copilot"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"github.com/gesta-run/subpool/internal/store"
)

type testCipher struct{ plaintext []byte }

func (c testCipher) Decrypt([]byte) ([]byte, error) { return c.plaintext, nil }

type testCompatible struct{ status int }

type testBailianUsage struct {
	err   error
	calls int
}

func (b *testBailianUsage) Usage(context.Context, bailian.Credentials) (bailian.Snapshot, error) {
	b.calls++
	return bailian.Snapshot{PlanType: bailian.PlanType, Weekly: &bailian.Window{RemainingPercent: 75}}, b.err
}

func TestBailianQuotaPermissionFailurePreservesLastKnownQuotaAndRouting(t *testing.T) {
	credentials, _ := json.Marshal(openaicompat.Credentials{
		BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "illustrative-key",
		BailianQuota: &bailian.Credentials{AccessKeyID: "illustrative-id", AccessKeySecret: "illustrative-secret"},
	})
	oldQuota := json.RawMessage(`{"weekly":{"remaining_percent":75}}`)
	st := &quotaFailureStore{account: domain.ProviderAccount{ID: "account", Provider: domain.ProviderOpenAICompatible, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy, QuotaSnapshot: oldQuota}}
	checker := NewChecker(st, testCipher{plaintext: credentials}, nil, testCompatible{status: 200})
	checker.bailian = &testBailianUsage{err: bailian.ErrPermissionDenied}
	checked, err := checker.CheckAccount(context.Background(), "account")
	if err != nil {
		t.Fatal(err)
	}
	if checked.Status != domain.AccountActive || checked.HealthStatus != domain.HealthHealthy || string(checked.QuotaSnapshot) != string(oldQuota) || st.healthFailures != 0 || st.quotaError != "quota_permission_denied" {
		t.Fatalf("routing or last known quota changed: %+v", checked)
	}
}

func TestBailianQuotaAuthorizationIsIndependentOfInference(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		credentials := openaicompat.Credentials{BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "illustrative-key"}
		if authorized {
			credentials.BailianQuota = &bailian.Credentials{AccessKeyID: "illustrative-id", AccessKeySecret: "illustrative-secret"}
		}
		plaintext, _ := json.Marshal(credentials)
		checker := NewChecker(nil, testCipher{plaintext: plaintext}, nil, testCompatible{status: 200})
		usage := &testBailianUsage{}
		checker.bailian = usage
		result := checker.Check(context.Background(), domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible})
		if result.HealthStatus != domain.HealthHealthy || result.AuthFailed || result.Failure || result.UsageAllowed != nil {
			t.Fatalf("quota altered inference: %+v", result)
		}
		if authorized && (usage.calls != 1 || len(result.QuotaSnapshot) == 0) {
			t.Fatal("quota not fetched")
		}
		if !authorized && (usage.calls != 0 || result.QuotaErrorCode != "quota_authorization_required") {
			t.Fatal("missing credentials silently accepted")
		}
	}
}

func TestExpiredBrowserQuotaAuthorizationDoesNotDisableInference(t *testing.T) {
	plaintext, _ := json.Marshal(openaicompat.Credentials{
		BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", APIKey: "illustrative-key",
		BailianQuota: &bailian.Credentials{AccessToken: "illustrative-token"},
	})
	checker := NewChecker(nil, testCipher{plaintext: plaintext}, nil, testCompatible{status: 200})
	checker.bailian = &testBailianUsage{err: bailian.ErrAuthorizationExpired}
	result := checker.Check(context.Background(), domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible})
	if result.HealthStatus != domain.HealthHealthy || result.AuthFailed || result.Failure || result.QuotaErrorCode != "quota_authorization_expired" {
		t.Fatalf("expired quota authorization affected inference: %+v", result)
	}
}

func (c testCompatible) Models(context.Context, openaicompat.Credentials) (*http.Response, error) {
	return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

type testCodexUsage struct {
	snapshot codex.UsageSnapshot
	err      error
}

type testCopilotCredits struct {
	snapshot copilot.CreditsSnapshot
	err      error
}

func (c testCopilotCredits) Credits(context.Context, copilot.Credentials) (copilot.CreditsSnapshot, error) {
	return c.snapshot, c.err
}

func (c testCodexUsage) Usage(context.Context, codex.Credentials) (codex.UsageSnapshot, error) {
	return c.snapshot, c.err
}

func TestCodexHealthCheckReportsExhaustedUsage(t *testing.T) {
	credentials, _ := json.Marshal(codex.Credentials{AccessToken: "access", AccountID: "account"})
	allowed := false
	account := domain.ProviderAccount{Provider: domain.ProviderCodex, Status: domain.AccountActive, CredentialCiphertext: []byte("encrypted")}
	checker := NewChecker(nil, testCipher{plaintext: credentials}, testCodexUsage{snapshot: codex.UsageSnapshot{UsageAllowed: &allowed, LimitReason: "rate_limit_reached"}}, nil)
	result := checker.Check(context.Background(), account)
	if result.HealthStatus != domain.HealthHealthy || result.UsageAllowed == nil || *result.UsageAllowed {
		t.Fatalf("result = %#v", result)
	}
	checker.ApplyNewAccount(&account, result)
	if account.Status != domain.AccountExhausted {
		t.Fatalf("status = %q, want exhausted", account.Status)
	}
	if account.QuotaCheckedAt == nil || account.LastQuotaErrorCode != "" {
		t.Fatalf("quota freshness = %#v", account)
	}
}

func TestCodexHealthCheckTracksQuotaProbeFailure(t *testing.T) {
	credentials, _ := json.Marshal(codex.Credentials{AccessToken: "access", AccountID: "account"})
	account := domain.ProviderAccount{Provider: domain.ProviderCodex, CredentialCiphertext: []byte("encrypted")}
	checker := NewChecker(nil, testCipher{plaintext: credentials}, testCodexUsage{err: errors.New("start Codex app-server: executable not found")}, nil)
	result := checker.Check(context.Background(), account)
	if result.HealthStatus != domain.HealthUnknown || result.ErrorCode != "" || result.QuotaErrorCode != "connection_failed" || !result.QuotaProbeFailed || result.Failure {
		t.Fatalf("result = %#v", result)
	}
	checker.ApplyNewAccount(&account, result)
	if account.LastQuotaErrorCode != "connection_failed" || account.QuotaCheckedAt != nil || account.LastCheckedAt != nil || account.ConsecutiveFailures != 0 {
		t.Fatalf("quota freshness = %#v", account)
	}
}

func TestCopilotHealthCheckStoresAICreditsAndExhaustsRouting(t *testing.T) {
	credentials, _ := json.Marshal(copilot.Credentials{GitHubToken: "github-token", Email: "octocat@example.com"})
	allowed := false
	account := domain.ProviderAccount{Provider: domain.ProviderCopilot, Status: domain.AccountActive, CredentialCiphertext: []byte("encrypted")}
	snapshot := copilot.CreditsSnapshot{
		PlanType: "pro_plus", UsageAllowed: &allowed,
		Credits: &copilot.CreditsQuota{Used: 1500, Entitlement: 1500, RemainingPercent: 0},
	}
	checker := NewChecker(nil, testCipher{plaintext: credentials}, nil, nil, testCopilotCredits{snapshot: snapshot})
	result := checker.Check(context.Background(), account)
	if result.HealthStatus != domain.HealthHealthy || result.UsageAllowed == nil || *result.UsageAllowed || !strings.Contains(string(result.QuotaSnapshot), `"credits"`) {
		t.Fatalf("result = %#v", result)
	}
	checker.ApplyNewAccount(&account, result)
	if account.Status != domain.AccountExhausted || account.QuotaCheckedAt == nil || account.Email != "octocat@example.com" {
		t.Fatalf("account = %#v", account)
	}
}

func TestCopilotQuotaProbeFailurePreservesRoutingHealth(t *testing.T) {
	credentials, _ := json.Marshal(copilot.Credentials{GitHubToken: "github-token"})
	account := domain.ProviderAccount{Provider: domain.ProviderCopilot, CredentialCiphertext: []byte("encrypted")}
	checker := NewChecker(nil, testCipher{plaintext: credentials}, nil, nil, testCopilotCredits{err: context.DeadlineExceeded})
	result := checker.Check(context.Background(), account)
	if result.HealthStatus != domain.HealthUnknown || result.ErrorCode != "" || result.QuotaErrorCode != "timeout" || !result.QuotaProbeFailed || result.Failure {
		t.Fatalf("result = %#v", result)
	}
}

func TestCopilotIncompleteCredentialsRemainAHealthFailure(t *testing.T) {
	credentials, _ := json.Marshal(copilot.Credentials{})
	account := domain.ProviderAccount{Provider: domain.ProviderCopilot, CredentialCiphertext: []byte("encrypted")}
	checker := NewChecker(nil, testCipher{plaintext: credentials}, nil, nil, testCopilotCredits{err: copilot.ErrCredentialsIncomplete})
	result := checker.Check(context.Background(), account)
	if result.HealthStatus != domain.HealthUnhealthy || result.ErrorCode != "credential_unavailable" || !result.Failure || result.QuotaProbeFailed {
		t.Fatalf("result = %#v", result)
	}
}

type quotaFailureStore struct {
	store.Store
	account        domain.ProviderAccount
	quotaError     string
	healthUpdates  int
	healthFailures int
}

func (s *quotaFailureStore) ReactivateProviderIfCooldownExpired(_ context.Context, _ string, now time.Time) (bool, error) {
	if s.account.Status != domain.AccountCoolingDown || s.account.CooldownUntil == nil || s.account.CooldownUntil.After(now) {
		return false, nil
	}
	s.account.Status = domain.AccountActive
	s.account.CooldownUntil = nil
	return true, nil
}

func (s *quotaFailureStore) GetProviderAccount(context.Context, string) (domain.ProviderAccount, error) {
	return s.account, nil
}

func (s *quotaFailureStore) SetProviderQuotaError(_ context.Context, _ string, code string, next time.Time) error {
	s.quotaError = code
	s.account.LastQuotaErrorCode = code
	s.account.NextHealthCheckAt = &next
	return nil
}

func (s *quotaFailureStore) SetProviderHealth(context.Context, string, string, string, time.Time, time.Time) error {
	s.healthUpdates++
	return nil
}

func (s *quotaFailureStore) RecordProviderHealthFailure(context.Context, string, string, time.Time, time.Time) error {
	s.healthFailures++
	return nil
}

func TestCodexQuotaProbeFailurePreservesRoutingHealth(t *testing.T) {
	credentials, _ := json.Marshal(codex.Credentials{AccessToken: "access", AccountID: "account"})
	st := &quotaFailureStore{account: domain.ProviderAccount{
		ID: "account-1", Provider: domain.ProviderCodex, CredentialCiphertext: []byte("encrypted"),
		HealthStatus: domain.HealthHealthy, ConsecutiveFailures: 2, LastHealthErrorCode: "provider_5xx",
	}}
	checker := NewChecker(st, testCipher{plaintext: credentials}, testCodexUsage{err: context.DeadlineExceeded}, nil)
	checked, err := checker.CheckAccount(context.Background(), st.account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.quotaError != "timeout" || st.healthUpdates != 0 || st.healthFailures != 0 {
		t.Fatalf("quota error=%q health updates=%d health failures=%d", st.quotaError, st.healthUpdates, st.healthFailures)
	}
	if checked.HealthStatus != domain.HealthHealthy || checked.ConsecutiveFailures != 2 || checked.LastHealthErrorCode != "provider_5xx" {
		t.Fatalf("routing health changed: %#v", checked)
	}
}

func TestCheckAccountReactivatesExpiredCooldown(t *testing.T) {
	now := time.Date(2026, 9, 17, 8, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	credentials, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://example.com/v1", APIKey: "test"})
	st := &quotaFailureStore{account: domain.ProviderAccount{
		ID: "account-1", Provider: domain.ProviderOpenAICompatible, CredentialType: domain.CredentialAPIKey,
		CredentialCiphertext: []byte("encrypted"), Status: domain.AccountCoolingDown, CooldownUntil: &expired,
	}}
	checker := NewChecker(st, testCipher{plaintext: credentials}, nil, testCompatible{status: http.StatusOK})
	checker.now = func() time.Time { return now }

	checked, err := checker.CheckAccount(context.Background(), st.account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if checked.Status != domain.AccountActive || checked.CooldownUntil != nil {
		t.Fatalf("account was not reactivated: %#v", checked)
	}
}

func TestCompatibleHealthCheck(t *testing.T) {
	credentials, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.example.com/v1", APIKey: "sk-test-placeholder"})
	account := domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible, CredentialCiphertext: []byte("encrypted")}
	healthy := NewChecker(nil, testCipher{plaintext: credentials}, nil, testCompatible{status: http.StatusOK}).Check(context.Background(), account)
	if healthy.HealthStatus != domain.HealthHealthy || healthy.ErrorCode != "" {
		t.Fatalf("healthy result = %#v", healthy)
	}
	unauthorized := NewChecker(nil, testCipher{plaintext: credentials}, nil, testCompatible{status: http.StatusUnauthorized}).Check(context.Background(), account)
	if unauthorized.HealthStatus != domain.HealthUnhealthy || !unauthorized.AuthFailed || unauthorized.ErrorCode != "authentication_failed" {
		t.Fatalf("unauthorized result = %#v", unauthorized)
	}
	unsupported := NewChecker(nil, testCipher{plaintext: credentials}, nil, testCompatible{status: http.StatusNotFound}).Check(context.Background(), account)
	if unsupported.HealthStatus != domain.HealthUnknown || unsupported.Failure || unsupported.ErrorCode != "probe_unsupported" {
		t.Fatalf("unsupported result = %#v", unsupported)
	}
}

func TestClassifyCodexQuotaStatus(t *testing.T) {
	unauthorized := classifyCodexQuotaError(errors.New("request failed with status 401"))
	if unauthorized.ErrorCode != "authentication_failed" || unauthorized.QuotaErrorCode != "authentication_failed" || !unauthorized.AuthFailed || unauthorized.QuotaProbeFailed {
		t.Fatalf("unauthorized result = %#v", unauthorized)
	}
	rateLimited := classifyCodexQuotaError(errors.New("request failed with status 429"))
	if rateLimited.ErrorCode != "" || rateLimited.QuotaErrorCode != "quota_probe_rate_limited" || !rateLimited.QuotaProbeFailed || rateLimited.Failure {
		t.Fatalf("rate limited result = %#v", rateLimited)
	}
}

func TestClassifyTimeout(t *testing.T) {
	got := classifyError(errors.Join(errors.New("request failed"), context.DeadlineExceeded))
	want := Result{HealthStatus: domain.HealthUnknown, ErrorCode: "timeout", Failure: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v, want %#v", got, want)
	}
}
