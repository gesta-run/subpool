package store

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
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
	var currentBudget quotaBudget
	var currentPriority int32
	currentValid := false
	for _, c := range candidates {
		if c.ID == currentID {
			currentPriority = c.Priority
			currentBudget, currentValid = quotaRoutingBudget(c.QuotaSnapshot, optionalTime(c.QuotaCheckedAt), c.QuotaError, now)
		}
	}
	selected := currentID
	bestBudget := currentBudget
	bestPriority := currentPriority
	bestLoad := math.Inf(1)
	for _, c := range candidates {
		if c.ID == currentID {
			continue
		}
		budget, valid := quotaRoutingBudget(c.QuotaSnapshot, optionalTime(c.QuotaCheckedAt), c.QuotaError, now)
		if !valid {
			continue
		}
		// Subscription quota takes precedence over credits. Within the same tier,
		// keep explicit pool priorities and the existing subscription hysteresis.
		if !currentValid {
			if c.Priority >= currentPriority {
				continue
			}
		} else if !budget.preferredTo(currentBudget, c.Priority, currentPriority, true) {
			continue
		}
		capacity, capacityErr := q.ProviderAccountHasCapacity(ctx, c.ID)
		if capacityErr != nil {
			return domain.ProviderAccount{}, capacityErr
		}
		if !capacity {
			continue
		}
		load := float64(c.BoundKeys) / math.Max(1, float64(c.Weight))
		if selected == currentID || budget.preferredTo(bestBudget, c.Priority, bestPriority, false) || (budget == bestBudget && c.Priority == bestPriority && load < bestLoad) {
			selected, bestPriority, bestBudget, bestLoad = c.ID, c.Priority, budget, load
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

type quotaBudget struct {
	remaining float64
	plan      string
	credits   bool
}

func (b quotaBudget) preferredTo(current quotaBudget, priority, currentPriority int32, hysteresis bool) bool {
	if b.credits != current.credits {
		return !b.credits
	}
	if priority != currentPriority {
		return priority < currentPriority
	}
	margin := 0.0
	if !b.credits {
		if b.plan != current.plan {
			return false
		}
		if hysteresis {
			margin = quotaRoutingMargin
		}
	}
	return b.remaining > current.remaining+margin
}

// Require a fresh, complete snapshot before choosing between subscription quota
// and prepaid credits. Never infer renewed quota merely from an elapsed reset.
func quotaRoutingBudget(raw []byte, checked *time.Time, quotaError string, now time.Time) (quotaBudget, bool) {
	if checked == nil || checked.After(now) || now.Sub(*checked) > quotaRoutingMaxAge || quotaError != "" {
		return quotaBudget{}, false
	}
	type window struct {
		UsedPercent *float64 `json:"used_percent"`
		ResetAt     int64    `json:"reset_at"`
	}
	var quota struct {
		PlanType     string                 `json:"plan_type"`
		UsageAllowed *bool                  `json:"usage_allowed"`
		FiveHour     *window                `json:"five_hour"`
		Weekly       *window                `json:"weekly"`
		Credits      *codex.CreditsSnapshot `json:"credits"`
	}
	if json.Unmarshal(raw, &quota) != nil || quota.PlanType == "" || (quota.UsageAllowed != nil && !*quota.UsageAllowed) {
		return quotaBudget{}, false
	}
	budget := quotaBudget{remaining: 100, plan: quota.PlanType}
	windows := 0
	for _, w := range []*window{quota.FiveHour, quota.Weekly} {
		if w == nil {
			continue
		}
		if w.UsedPercent == nil || w.ResetAt <= now.Unix() || *w.UsedPercent < 0 || *w.UsedPercent > 100 || math.IsNaN(*w.UsedPercent) {
			return quotaBudget{}, false
		}
		windows++
		budget.remaining = math.Min(budget.remaining, 100-*w.UsedPercent)
	}
	if windows == 0 {
		return quotaBudget{}, false
	}
	if budget.remaining == 0 {
		budget.remaining = quota.Credits.PrepaidBalance()
		budget.credits = true
		if budget.remaining == 0 {
			return quotaBudget{}, false
		}
	}
	return budget, true
}

// Keep initial assignments and retry/failover selections on the same policy as
// independent requests. The caller already holds the pool assignment lock.
func selectQuotaAccount(ctx context.Context, q *storedb.Queries, poolID, initialID string, excluded []string) (string, error) {
	candidates, err := q.LockQuotaRoutingCandidates(ctx, storedb.LockQuotaRoutingCandidatesParams{PoolID: poolID, CurrentID: initialID})
	if err != nil {
		return "", err
	}
	now := time.Now()
	selected := initialID
	var best quotaBudget
	var priority int32
	valid := false
	// The allocator already applied weighted key load and checked this account's
	// capacity. Keep its choice when budgets are equal.
	for _, c := range candidates {
		if c.ID == initialID {
			best, valid = quotaRoutingBudget(c.QuotaSnapshot, optionalTime(c.QuotaCheckedAt), c.QuotaError, now)
			priority = c.Priority
			break
		}
	}
	for _, c := range candidates {
		skip := false
		for _, id := range excluded {
			if c.ID == id {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		budget, ok := quotaRoutingBudget(c.QuotaSnapshot, optionalTime(c.QuotaCheckedAt), c.QuotaError, now)
		if !ok || (valid && !budget.preferredTo(best, c.Priority, priority, false)) {
			continue
		}
		capacity, err := q.ProviderAccountHasCapacity(ctx, c.ID)
		if err != nil {
			return "", err
		}
		if capacity {
			selected, best, priority, valid = c.ID, budget, c.Priority, true
		}
	}
	return selected, nil
}
