package health

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

func pollingCredentials() openaibilling.Credentials {
	return openaibilling.Credentials{AdminKey: "sk-admin-example", OrganizationID: "org-example", GrantAmount: 50, GrantStartAt: 1800000000, GrantExpiresAt: 1800600000}
}

func TestBillingCachePreservesTimestampAndManualRefresh(t *testing.T) {
	now := time.Unix(1800200000, 0)
	checked := now.Add(-20 * time.Minute)
	snapshot := json.RawMessage(`{"plan_type":"openai_api_credits","balance":{"estimated":true,"remaining_usd":37.5}}`)
	credentials := pollingCredentials()
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.openai.com/v1", APIKey: "example", OpenAIBilling: &credentials})
	st := &quotaFailureStore{account: domain.ProviderAccount{ID: "example", Provider: domain.ProviderOpenAICompatible, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy, QuotaSnapshot: snapshot, QuotaCheckedAt: &checked}}
	c := NewChecker(st, testCipher{plaintext: raw}, nil, testCompatible{status: 200})
	c.now = func() time.Time { return now }
	c.openaiBalance = balanceProbe{}
	result := c.Check(context.Background(), st.account)
	if !result.QuotaSkipped || len(result.QuotaSnapshot) != 0 {
		t.Fatal("fresh Costs snapshot was unnecessarily fetched")
	}
	if err := c.persist(context.Background(), st.account.ID, result); err != nil {
		t.Fatal(err)
	}
	if !st.account.QuotaCheckedAt.Equal(checked) || string(st.account.QuotaSnapshot) != string(snapshot) {
		t.Fatal("skipped poll changed the saved snapshot or fetch time")
	}
	if result = c.check(context.Background(), st.account, true); result.QuotaSkipped || len(result.QuotaSnapshot) == 0 {
		t.Fatal("manual refresh did not bypass the normal cache interval")
	}
}

func TestBillingRateLimitBackoffRespectsServerAndCredentialChanges(t *testing.T) {
	now := time.Unix(1800200000, 0)
	c := NewChecker(nil, nil, nil, nil)
	c.now = func() time.Time { return now }
	account := domain.ProviderAccount{ID: "example", LastQuotaErrorCode: "quota_probe_rate_limited"}
	credentials := pollingCredentials()
	for _, delay := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute} {
		if !c.claimBillingPoll(account, credentials, false) {
			t.Fatal("due poll was skipped")
		}
		c.finishBillingPoll(account.ID, credentials, &openaibilling.RateLimitError{Code: "rate_limit_exceeded"})
		if c.claimBillingPoll(account, credentials, true) {
			t.Fatal("manual refresh bypassed rate-limit backoff")
		}
		now = now.Add(delay)
	}
	if !c.claimBillingPoll(account, credentials, false) {
		t.Fatal("backoff never recovered")
	}
	c.finishBillingPoll(account.ID, credentials, &openaibilling.RateLimitError{RetryAfter: 2 * time.Hour})
	now = now.Add(time.Hour)
	if c.claimBillingPoll(account, credentials, true) {
		t.Fatal("Retry-After was ignored")
	}
	credentials.AdminKey = "sk-admin-replacement"
	if !c.claimBillingPoll(account, credentials, true) {
		t.Fatal("replaced credentials inherited the old backoff")
	}
	c.finishBillingPoll(account.ID, credentials, nil)
	if c.billingPolls[account.ID].failures != 0 {
		t.Fatal("successful refresh did not reset backoff")
	}
}

func TestConcurrentBillingPollsShareOneRequest(t *testing.T) {
	c := NewChecker(nil, nil, nil, nil)
	var group sync.WaitGroup
	results := make(chan bool, 20)
	for i := 0; i < cap(results); i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- c.claimBillingPoll(domain.ProviderAccount{ID: "example"}, pollingCredentials(), true)
		}()
	}
	group.Wait()
	close(results)
	claimed := 0
	for ok := range results {
		if ok {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent requests claimed %d reads", claimed)
	}
}

func TestSkippedBackoffPreservesQuotaErrorAndModelHealth(t *testing.T) {
	now := time.Unix(1800200000, 0)
	credentials := pollingCredentials()
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.openai.com/v1", APIKey: "example", OpenAIBilling: &credentials})
	st := &quotaFailureStore{account: domain.ProviderAccount{ID: "example", Provider: domain.ProviderOpenAICompatible, Status: domain.AccountActive, HealthStatus: domain.HealthHealthy}}
	c := NewChecker(st, testCipher{plaintext: raw}, nil, testCompatible{status: 200})
	c.now = func() time.Time { return now }
	c.openaiBalance = balanceProbe{err: &openaibilling.RateLimitError{Code: "rate_limit_exceeded", RetryAfter: time.Hour}}
	result := c.Check(context.Background(), st.account)
	if result.QuotaErrorCode != "quota_probe_rate_limited" || result.HealthStatus != domain.HealthHealthy || result.AuthFailed || result.Failure {
		t.Fatalf("rate limit changed inference health: %+v", result)
	}
	if err := c.persist(context.Background(), st.account.ID, result); err != nil {
		t.Fatal(err)
	}
	result = c.Check(context.Background(), st.account)
	if !result.QuotaSkipped {
		t.Fatal("rate-limited request was repeated during backoff")
	}
	if err := c.persist(context.Background(), st.account.ID, result); err != nil {
		t.Fatal(err)
	}
	if st.quotaError != "quota_probe_rate_limited" || st.account.Status != domain.AccountActive {
		t.Fatal("skipped poll erased the error or disabled routing")
	}
}

type scheduledBillingStore struct{ quotaFailureStore }

func (s *scheduledBillingStore) ClaimProviderHealthChecks(context.Context, int, time.Time, time.Time) ([]domain.ProviderAccount, error) {
	// Match the real claim query: it intentionally returns no quota fields.
	return []domain.ProviderAccount{{ID: s.account.ID, Provider: s.account.Provider, Status: s.account.Status}}, nil
}

func TestScheduledBillingReadsSavedQuotaBeforePolling(t *testing.T) {
	now := time.Unix(1800200000, 0)
	checked := now.Add(-20 * time.Minute)
	credentials := pollingCredentials()
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.openai.com/v1", APIKey: "example", OpenAIBilling: &credentials})
	snapshot := json.RawMessage(`{"plan_type":"openai_api_credits","balance":{"estimated":true,"remaining_usd":37.5}}`)
	st := &scheduledBillingStore{quotaFailureStore{account: domain.ProviderAccount{ID: "example", Provider: domain.ProviderOpenAICompatible, Status: domain.AccountActive, QuotaCheckedAt: &checked, QuotaSnapshot: snapshot}}}
	c := NewChecker(st, testCipher{plaintext: raw}, nil, testCompatible{status: 200})
	c.now = func() time.Time { return now }
	// Any unexpected billing read would persist this error instead of using cache.
	c.openaiBalance = balanceProbe{err: openaibilling.ErrUnavailable}
	c.runBatch(context.Background())
	if st.quotaError != "" || st.healthUpdates != 1 || !st.account.QuotaCheckedAt.Equal(checked) || string(st.account.QuotaSnapshot) != string(snapshot) {
		t.Fatal("scheduled cache failed or changed the last successful snapshot")
	}
}
