package openaibilling

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestCostsRateLimitKeepsOnlySafeCodeAndRetryAfter(t *testing.T) {
	now := time.Unix(1800200000, 0)
	for _, tc := range []struct {
		header string
		code   string
		want   time.Duration
	}{
		{"", "rate_limit_exceeded", time.Minute},
		{"120", "slow_down", 2 * time.Minute},
		{now.Add(3 * time.Minute).UTC().Format(http.TimeFormat), "rate_limit_exceeded", 3 * time.Minute},
		{"-1", "sk-admin-example-secret", time.Minute},
	} {
		t.Run(tc.header+tc.code, func(t *testing.T) {
			c := NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				r := response(429, `{"error":{"code":"`+tc.code+`","message":"sk-admin-example-secret org-example"}}`)
				r.Header.Set("Retry-After", tc.header)
				return r, nil
			})})
			c.now = func() time.Time { return now }
			got, err := c.Usage(context.Background(), exampleCostsCredentials())
			var limited *RateLimitError
			if !errors.As(err, &limited) || !errors.Is(err, ErrUnavailable) || limited.RetryAfter != tc.want || got.PlanType != "" {
				t.Fatalf("unexpected rate-limit result: %+v %v", got, err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "org-example") {
				t.Fatal("upstream details leaked")
			}
		})
	}
}
