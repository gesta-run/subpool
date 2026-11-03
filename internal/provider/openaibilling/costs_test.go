package openaibilling

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func exampleCostsCredentials() Credentials {
	// All keys, amounts and timestamps here are illustrative.
	return Credentials{AdminKey: "sk-admin-illustrative", OrganizationID: "org-example", GrantAmount: 50, GrantStartAt: 1800000000, GrantExpiresAt: 1800600000}
}

func TestCostsEstimateReadsAllPagesAndUsesAdminKey(t *testing.T) {
	calls := 0
	client := NewClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.openai.com" || r.URL.Path != "/v1/organization/costs" || r.Header.Get("Authorization") != "Bearer sk-admin-illustrative" || r.Header.Get("OpenAI-Organization") != "org-example" || r.URL.Query().Get("start_time") != "1800000000" || r.URL.Query().Has("project_ids") {
			t.Fatal("incorrect cost scope")
		}
		if r.URL.Query().Get("page") == "" {
			return response(200, `{"data":[{"end_time":1800086400,"results":[{"object":"organization.costs.result","amount":{"currency":"usd","value":8}}]}],"has_more":true,"next_page":"next & page"}`), nil
		}
		if r.URL.Query().Get("page") != "next & page" {
			t.Fatal("cursor was not encoded")
		}
		return response(200, `{"data":[{"end_time":1800172800,"results":[{"object":"organization.costs.result","amount":{"currency":"usd","value":4.5}}]}],"has_more":false}`), nil
	})})
	client.now = func() time.Time { return time.Unix(1800200000, 0) }
	got, err := client.Usage(context.Background(), exampleCostsCredentials())
	if err != nil || calls != 2 || !got.Balance.Estimated || got.Balance.Remaining != 37.5 || got.Balance.Used != 12.5 || got.Balance.UsageThrough != 1800172800 || got.Balance.GrantStartAt != 1800000000 {
		t.Fatalf("unexpected estimate: %+v %v calls=%d", got, err, calls)
	}
}

func TestCostsRejectsMissingAmountsAndPartialPagination(t *testing.T) {
	for _, body := range []string{`{}`, `{"data":[] ,"has_more":true}`, `{"data":[{"end_time":1800086400,"results":[{"object":"organization.costs.result","amount":{"currency":"eur","value":2}}]}]}`, `{"data":[{"end_time":1800086400,"results":[{"object":"organization.costs.result","amount":{"currency":"usd"}}]}]}`} {
		c := NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(200, body), nil })})
		c.now = func() time.Time { return time.Unix(1800200000, 0) }
		if got, err := c.Usage(context.Background(), exampleCostsCredentials()); !errors.Is(err, ErrInvalidResponse) || got.PlanType != "" {
			t.Fatalf("invalid cost data accepted: %+v %v", got, err)
		}
	}
	calls := 0
	c := NewClient(&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response(200, `{"data":[],"has_more":true,"next_page":"example"}`), nil
		}
		return response(403, `{"error":"sk-admin-illustrative"}`), nil
	})})
	c.now = func() time.Time { return time.Unix(1800200000, 0) }
	if got, err := c.Usage(context.Background(), exampleCostsCredentials()); !errors.Is(err, ErrAuthorizationExpired) || got.PlanType != "" {
		t.Fatal("partial estimate returned")
	}
}

func TestCostsGrantExpiryAndExhaustion(t *testing.T) {
	c := NewClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return response(200, `{"data":[{"end_time":1800600000,"results":[{"object":"organization.costs.result","amount":{"currency":"usd","value":60}}]}]}`), nil
	})})
	c.now = func() time.Time { return time.Unix(1800200000, 0) }
	got, err := c.Usage(context.Background(), exampleCostsCredentials())
	if err != nil || got.Balance.Remaining != 0 || got.Balance.UsageThrough != 1800200000 {
		t.Fatal("exhausted balance or data cutoff incorrect")
	}
	c.now = func() time.Time { return time.Unix(1800700000, 0) }
	creds := exampleCostsCredentials()
	creds.GrantAmount = 100
	got, err = c.Usage(context.Background(), creds)
	if err != nil || got.Balance.Remaining != 0 {
		t.Fatal("expired grant retained balance")
	}
	creds.SessionKey = "also-a-session"
	if creds.Valid() {
		t.Fatal("ambiguous credential modes accepted")
	}
	creds.SessionKey = ""
	creds.GrantExpiresAt = creds.GrantStartAt
	if creds.Valid() {
		t.Fatal("invalid grant period accepted")
	}
}
