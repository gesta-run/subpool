package health

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
)

type billingPoll struct {
	identity [32]byte
	retryAt  time.Time
	failures int
	inFlight bool
}

func billingIdentity(credentials openaibilling.Credentials) [32]byte {
	raw, _ := json.Marshal(credentials)
	return sha256.Sum256(raw)
}

// Successful snapshots live in the existing account row. Skipping a poll must
// preserve both its actual fetch timestamp and any outstanding quota error.
func (c *Checker) claimBillingPoll(account domain.ProviderAccount, credentials openaibilling.Credentials, force bool) bool {
	c.billingMu.Lock()
	defer c.billingMu.Unlock()
	if c.billingPolls == nil {
		c.billingPolls = make(map[string]billingPoll)
	}
	identity := billingIdentity(credentials)
	poll := c.billingPolls[account.ID]
	if poll.identity != identity {
		poll = billingPoll{identity: identity}
	}
	now := c.now()
	if poll.inFlight || now.Before(poll.retryAt) {
		return false
	}
	if !force && account.LastQuotaErrorCode == "" && account.QuotaCheckedAt != nil && !account.QuotaCheckedAt.After(now) && now.Sub(*account.QuotaCheckedAt) < openaibilling.RefreshInterval {
		var snapshot openaibilling.Snapshot
		if json.Unmarshal(account.QuotaSnapshot, &snapshot) == nil && snapshot.PlanType == openaibilling.PlanType && snapshot.Balance.Estimated && (snapshot.Balance.ExpiresAt == 0 || snapshot.Balance.ExpiresAt > now.Unix()) {
			return false
		}
	}
	poll.inFlight = true
	c.billingPolls[account.ID] = poll
	return true
}

func (c *Checker) finishBillingPoll(accountID string, credentials openaibilling.Credentials, err error) {
	c.billingMu.Lock()
	defer c.billingMu.Unlock()
	poll := c.billingPolls[accountID]
	if poll.identity != billingIdentity(credentials) {
		return
	}
	poll.inFlight = false
	var limited *openaibilling.RateLimitError
	if errors.As(err, &limited) {
		poll.failures++
		delay := time.Minute
		for i := 1; i < poll.failures && delay < openaibilling.RefreshInterval; i++ {
			delay *= 2
		}
		if delay > openaibilling.RefreshInterval {
			delay = openaibilling.RefreshInterval
		}
		if limited.RetryAfter > delay {
			delay = limited.RetryAfter
		}
		poll.retryAt = c.now().Add(delay)
	} else {
		poll.failures = 0
		poll.retryAt = time.Time{}
	}
	c.billingPolls[accountID] = poll
}
