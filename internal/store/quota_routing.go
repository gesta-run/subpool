package store

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/store/storedb"
)

const quotaRoutingMargin = 5.0
const quotaRoutingMaxAge = 15 * time.Minute

// WithCodexQuotaRouting configures the opt-in policy before serving requests.
func (p *Postgres) WithCodexQuotaRouting(enabled bool) *Postgres {
	p.codexQuotaRouting = enabled
	return p
}

// PreferQuotaAccount must only be called before an independent request or the
// first turn of an unbound connection. It never modifies response bindings.
func (p *Postgres) PreferQuotaAccount(ctx context.Context, route domain.KeyRoute) (domain.ProviderAccount, error) {
	if !p.codexQuotaRouting || route.Account.Provider != domain.ProviderCodex {
		return route.Account, nil
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return domain.ProviderAccount{}, wrapDB("begin quota routing", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := p.queries.WithTx(tx)
	if err = q.LockPoolAssignments(ctx, route.Pool.ID); err != nil {
		return domain.ProviderAccount{}, err
	}
	// A concurrent request may already have changed the default binding.
	currentID, err := q.GetQuotaRoutingBinding(ctx, storedb.GetQuotaRoutingBindingParams{ApiKeyID: route.Key.ID, PoolID: route.Pool.ID})
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	candidates, err := q.LockQuotaRoutingCandidates(ctx, storedb.LockQuotaRoutingCandidatesParams{PoolID: route.Pool.ID, CurrentID: currentID})
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	now := time.Now()
	var currentScore float64
	var currentPlan string
	currentValid := false
	for _, c := range candidates {
		if c.ID == currentID {
			currentScore, currentPlan, currentValid = quotaRoutingScore(c.QuotaSnapshot, optionalTime(c.QuotaCheckedAt), c.QuotaError, now)
		}
	}
	selected := currentID
	bestScore := currentScore
	bestLoad := math.Inf(1)
	if currentValid {
		for _, c := range candidates {
			if c.ID == currentID {
				continue
			}
			score, plan, valid := quotaRoutingScore(c.QuotaSnapshot, optionalTime(c.QuotaCheckedAt), c.QuotaError, now)
			if !valid || plan != currentPlan || score <= currentScore+quotaRoutingMargin {
				continue
			}
			// Capacity is checked in a new statement after acquiring account locks so
			// assignments from other pools committed while we waited are visible.
			capacity, capacityErr := q.ProviderAccountHasCapacity(ctx, c.ID)
			if capacityErr != nil {
				return domain.ProviderAccount{}, capacityErr
			}
			if !capacity {
				continue
			}
			load := float64(c.BoundKeys) / math.Max(1, float64(c.Weight))
			if selected == currentID || score > bestScore || (score == bestScore && load < bestLoad) {
				selected, bestScore, bestLoad = c.ID, score, load
			}
		}
	}
	if selected != currentID {
		if err = q.ReassignAPIKey(ctx, storedb.ReassignAPIKeyParams{ProviderAccountID: selected, ApiKeyID: route.Key.ID}); err != nil {
			return domain.ProviderAccount{}, err
		}
	}
	row, err := q.GetRoutableProviderAccount(ctx, selected)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProviderAccount{}, err
	}
	if selected != currentID {
		p.quotaRoutingSwitches.Add(1)
	}
	return routeAccount(row.ID, row.Provider, row.CredentialType, row.DisplayName,
		row.CredentialCiphertext, row.CredentialVersion, row.Status, row.FastModeEnabled,
		row.HealthStatus, row.QuotaSnapshot, row.QuotaCheckedAt, row.LastQuotaErrorCode,
		row.CooldownUntil, row.LastSuccessAt, row.LastFailureAt, row.CreatedAt, row.UpdatedAt), nil
}

// QuotaRoutingSwitches counts committed default-binding changes, not attempts.
func (p *Postgres) QuotaRoutingSwitches() uint64 { return p.quotaRoutingSwitches.Load() }

// Require complete, fresh windows; missing data must never look like free quota.
func quotaRoutingScore(raw []byte, checked *time.Time, quotaError string, now time.Time) (float64, string, bool) {
	if checked == nil || checked.After(now) || now.Sub(*checked) > quotaRoutingMaxAge || quotaError != "" {
		return 0, "", false
	}
	type window struct {
		UsedPercent *float64 `json:"used_percent"`
		ResetAt     int64    `json:"reset_at"`
	}
	var quota struct {
		PlanType     string  `json:"plan_type"`
		UsageAllowed *bool   `json:"usage_allowed"`
		FiveHour     *window `json:"five_hour"`
		Weekly       *window `json:"weekly"`
	}
	if json.Unmarshal(raw, &quota) != nil || quota.PlanType == "" || (quota.UsageAllowed != nil && !*quota.UsageAllowed) {
		return 0, "", false
	}
	score := 100.0
	for _, w := range []*window{quota.FiveHour, quota.Weekly} {
		if w == nil || w.UsedPercent == nil || w.ResetAt <= now.Unix() || *w.UsedPercent < 0 || *w.UsedPercent >= 100 || math.IsNaN(*w.UsedPercent) {
			return 0, "", false
		}
		score = math.Min(score, 100-*w.UsedPercent)
	}
	return score, quota.PlanType, true
}
