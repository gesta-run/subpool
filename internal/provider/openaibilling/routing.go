package openaibilling

import (
	"encoding/json"
	"math"
	"time"
)

// PromotionalBudget accepts only a bounded cached promotional grant. Billing
// estimates do not replace the upstream account's disabled paid billing policy.
func PromotionalBudget(raw []byte, checked *time.Time, quotaError string, now time.Time) (Balance, bool) {
	var snapshot Snapshot
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.PlanType != PlanType || snapshot.GrantDepletedAt > 0 {
		return Balance{}, false
	}
	b := snapshot.Balance
	if checked == nil || checked.After(now) || now.Sub(*checked) > 24*time.Hour ||
		(quotaError != "" && quotaError != "quota_probe_rate_limited") ||
		b.ExpiresAt <= now.Unix() || b.Granted <= 0 || b.Remaining <= 0 ||
		b.Remaining > b.Granted || b.PaidAvailable != 0 ||
		math.IsNaN(b.Remaining) || math.IsInf(b.Remaining, 0) ||
		math.IsNaN(b.Granted) || math.IsInf(b.Granted, 0) {
		return Balance{}, false
	}
	return b, true
}
