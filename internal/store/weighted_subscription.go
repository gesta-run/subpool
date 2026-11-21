package store

import (
	"time"

	"github.com/gesta-run/subpool/internal/store/storedb"
)

// weightedSubscription only selects fresh subscription capacity. An unlisted
// eligible subscription leaves the pool on its existing allocation policy.
func (p *Postgres) weightedSubscription(candidates []storedb.LockQuotaRoutingCandidatesRow, now time.Time) string {
	if len(p.codexRequestWeights) == 0 {
		return ""
	}
	type slot struct {
		accountID string
		weight    uint64
	}
	eligible := make([]slot, 0, len(candidates))
	var total uint64
	for _, c := range candidates {
		budget, ok := quotaRoutingBudget(c.QuotaSnapshot, optionalTime(c.QuotaCheckedAt), c.QuotaError, now)
		if !ok || budget.credits || budget.apiCredits {
			continue
		}
		weight := p.codexRequestWeights[c.ID]
		if weight == 0 {
			return ""
		}
		eligible = append(eligible, slot{c.ID, weight})
		total += weight
	}
	if len(eligible) == 0 {
		return ""
	}
	if len(eligible) == 1 {
		return eligible[0].accountID
	}
	ticket := (p.codexRequestSequence.Add(1) - 1) % total
	for _, candidate := range eligible {
		if ticket < candidate.weight {
			return candidate.accountID
		}
		ticket -= candidate.weight
	}
	return ""
}
