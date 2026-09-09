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

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type testCipher struct{ plaintext []byte }

func (c testCipher) Decrypt([]byte) ([]byte, error) { return c.plaintext, nil }

type testCompatible struct{ status int }

func (c testCompatible) Models(context.Context, openaicompat.Credentials) (*http.Response, error) {
	return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

type testCodexUsage struct{ snapshot codex.UsageSnapshot }

func (c testCodexUsage) Usage(context.Context, codex.Credentials) (codex.UsageSnapshot, error) {
	return c.snapshot, nil
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

func TestClassifyCodexStatusError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		want       Result
	}{
		{name: "unauthorized", statusCode: http.StatusUnauthorized, want: Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "authentication_failed", AuthFailed: true}},
		{name: "forbidden", statusCode: http.StatusForbidden, want: Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "authentication_failed", AuthFailed: true}},
		{name: "rate limited", statusCode: http.StatusTooManyRequests, want: Result{HealthStatus: domain.HealthHealthy}},
		{name: "not found", statusCode: http.StatusNotFound, want: Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_unavailable", Failure: true}},
		{name: "method not allowed", statusCode: http.StatusMethodNotAllowed, want: Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_unavailable", Failure: true}},
		{name: "provider failure", statusCode: http.StatusBadGateway, want: Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_5xx", Failure: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyError(&codex.HTTPStatusError{StatusCode: test.statusCode})
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestClassifyTimeout(t *testing.T) {
	got := classifyError(errors.Join(errors.New("request failed"), context.DeadlineExceeded))
	want := Result{HealthStatus: domain.HealthUnknown, ErrorCode: "timeout", Failure: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("result = %#v, want %#v", got, want)
	}
}
