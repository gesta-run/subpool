package openaibilling

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func TestBalanceUsesOnlyOfficialEndpointAndMatchingAuthorization(t *testing.T) {
	// All amounts and dates in these tests are illustrative.
	client := NewClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/dashboard/billing/credit_grants" || r.Header.Get("Authorization") != "Bearer example-session" || r.Header.Get("OpenAI-Organization") != "org-example" {
			t.Fatal("incorrect credential scope")
		}
		return response(200, `{"total_granted":50,"total_used":12.5,"total_available":37.5,"total_paid_available":0,"grants":{"data":[{"grant_amount":10,"used_amount":10,"expires_at":1800000100},{"grant_amount":40,"used_amount":2.5,"expires_at":1800001000},{"grant_amount":5,"used_amount":0,"expires_at":1800000500,"is_scheduled":true}]}}`), nil
	})})
	client.now = func() time.Time { return time.Unix(1800000000, 0) }
	got, err := client.Usage(context.Background(), Credentials{SessionKey: "example-session", OrganizationID: "org-example"})
	if err != nil || got.Balance.Remaining != 37.5 || got.Balance.ExpiresAt != 1800001000 {
		t.Fatalf("unexpected snapshot: %+v %v", got, err)
	}
}
func TestRejectsExpiredMalformedAndRedirectedBalanceWithoutLeakingSecrets(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		want error
	}{
		{401, `{"error":"example-session"}`, ErrAuthorizationExpired}, {403, `{}`, ErrAuthorizationExpired},
		{200, `{}`, ErrInvalidResponse}, {200, `<html>sign in</html>`, ErrInvalidResponse}, {429, `{}`, ErrUnavailable},
		{302, `{}`, ErrUnavailable},
	} {
		t.Run(http.StatusText(tc.code)+tc.body, func(t *testing.T) {
			calls := 0
			client := NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				calls++
				r := response(tc.code, tc.body)
				r.Header.Set("Location", "https://other.example/steal")
				return r, nil
			})})
			_, err := client.Usage(context.Background(), Credentials{SessionKey: "example-session", OrganizationID: "org-example"})
			if !errors.Is(err, tc.want) || calls != 1 || strings.Contains(err.Error(), "example-session") {
				t.Fatal("invalid balance accepted, followed redirect, or leaked credential")
			}
		})
	}
}
func TestVerifiesInferenceKeyOrganizationAndRejectsProxyURLs(t *testing.T) {
	client := NewClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/models" || r.Header.Get("Authorization") != "Bearer inference-key" {
			t.Fatal("used browser credentials to verify inference")
		}
		return response(403, `{}`), nil
	})})
	if !errors.Is(client.VerifyAccount(context.Background(), "inference-key", "org-wrong"), ErrOrganizationMismatch) {
		t.Fatal("organization mismatch accepted")
	}
	for _, url := range []string{"https://api.openai.com.evil.example/v1", "http://api.openai.com/v1", "https://api.openai.com/v1?foo=bar"} {
		if SupportsURL(url) {
			t.Fatal("unsafe endpoint accepted")
		}
	}
}
