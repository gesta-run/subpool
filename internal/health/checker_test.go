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
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"github.com/gesta-run/subpool/internal/store"
)

type testCipher struct{ plaintext []byte }

func (c testCipher) Decrypt([]byte) ([]byte, error) { return c.plaintext, nil }

type testCompatible struct{ status int }

func (c testCompatible) Models(context.Context, openaicompat.Credentials) (*http.Response, error) {
	return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

type testCodexUsage struct {
	snapshot codex.UsageSnapshot
	err      error
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

type quotaFailureStore struct {
	store.Store
	account        domain.ProviderAccount
	quotaError     string
	healthUpdates  int
	healthFailures int
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
