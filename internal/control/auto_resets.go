package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/reporting"
	"github.com/gesta-run/subpool/internal/store"
)

type autoResetStore interface {
	PlanReset(context.Context, store.ResetJob) error
	ClaimResetBatch(context.Context, time.Time, bool) (*store.ResetBatch, error)
	StartResetAttempt(context.Context, store.ResetJob) error
	FinishResetJob(context.Context, store.ResetJob) error
	FreezeResetReport(context.Context, string, []byte) ([]byte, error)
	FinishResetReport(context.Context, string, string) error
	RetryResetBatch(context.Context, string) error
}

// RunAutoResets discovers subscriptions automatically; no account-specific timer is required.
func (s *Server) RunAutoResets(ctx context.Context, recipient string, reporter *reporting.Service) {
	st, ok := s.store.(autoResetStore)
	if !ok {
		slog.Error("automatic resets require durable reset storage")
		return
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		slog.Error("automatic reset timezone unavailable")
		return
	}
	slog.Info("automatic Codex resets enabled", "timezone", location.String(), "window", "09:00–21:00")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		cycle, cancel := context.WithTimeout(ctx, 8*time.Minute)
		if err := s.discoverResets(cycle, st, time.Now(), location); err != nil {
			slog.Warn("automatic reset discovery failed", "error", err)
		}
		if err := s.processResetBatch(cycle, st, reporter, recipient, time.Now(), resetWindowOpen(time.Now(), location)); err != nil {
			slog.Error("automatic reset batch failed", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) discoverResets(ctx context.Context, st autoResetStore, now time.Time, location *time.Location) error {
	accounts, err := s.store.ListProviderAccounts(ctx)
	if err != nil {
		return err
	}
	for _, a := range accounts {
		if a.Provider != domain.ProviderCodex || a.CredentialType == domain.CredentialAPIKey || a.Status == domain.AccountDisabled {
			continue
		}
		snapshot, checked, readErr := s.store.GetProviderResetCredits(ctx, a.ID)
		if readErr != nil {
			return readErr
		}
		credits, decodeErr := decodeResetCredits(snapshot)
		if decodeErr != nil {
			return decodeErr
		}
		if checked == nil || now.Sub(*checked) > resetCreditsFreshFor {
			readCtx, readCancel := context.WithTimeout(ctx, 20*time.Second)
			fresh, _, refreshErr := s.refreshResetCredits(readCtx, a.ID)
			readCancel()
			if refreshErr == nil {
				credits = fresh
			} else {
				slog.Warn("automatic reset discovery used cached credits", "provider_account_id", a.ID)
			}
		}
		if credits == nil {
			continue
		}
		var first *codex.ResetCredit
		for _, c := range credits.Credits {
			if c.Status != "available" || c.ExpiresAt == nil || *c.ExpiresAt <= now.Unix() {
				continue
			}
			if first == nil || *c.ExpiresAt < *first.ExpiresAt {
				copy := c
				first = &copy
			}
		}
		if first == nil {
			continue
		}
		label := a.Email
		if label == "" {
			label = a.DisplayName
		}
		expires := time.Unix(*first.ExpiresAt, 0)
		if err := st.PlanReset(ctx, store.ResetJob{AccountID: a.ID, CreditID: first.ID, Label: label, RunAt: ResetTime(expires, location), ExpiresAt: expires}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) processResetBatch(ctx context.Context, st autoResetStore, reporter *reporting.Service, recipient string, now time.Time, allowNew bool) error {
	b, err := st.ClaimResetBatch(ctx, now, allowNew)
	if err != nil || b == nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.RetryResetBatch(cleanup, b.ID)
	}()
	if len(b.Payload) == 0 {
		groups := make(map[string][]int)
		for i := range b.Jobs {
			if b.Jobs[i].Outcome == "" {
				groups[b.Jobs[i].AccountID] = append(groups[b.Jobs[i].AccountID], i)
			}
		}
		var workers sync.WaitGroup
		gate := make(chan struct{}, 2)
		errors := make(chan error, len(groups))
		for _, indices := range groups {
			workers.Add(1)
			go func(indices []int) {
				defer workers.Done()
				select {
				case gate <- struct{}{}:
				case <-ctx.Done():
					errors <- ctx.Err()
					return
				}
				defer func() { <-gate }()
				for _, i := range indices {
					if err := s.runResetJob(ctx, st, &b.Jobs[i], allowNew); err != nil {
						errors <- err
						return
					}
				}
			}(indices)
		}
		workers.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				return err
			}
		}
		lines := []string{"Automatic reset results · Asia/Shanghai"}
		for _, j := range b.Jobs {
			lines = append(lines, fmt.Sprintf("%s: %s. Weekly capacity %s → %s. %s", j.Label, j.Outcome, resetCapacity(j.Before), resetCapacity(j.After), j.Detail))
		}
		m := reporter.EventMessage(ctx, time.Now(), "Reset results", strings.Join(lines, "\n"), recipient)
		encoded, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if b.Payload, err = st.FreezeResetReport(ctx, b.ID, encoded); err != nil {
			return err
		}
	}
	var m reporting.Message
	if err := json.Unmarshal(b.Payload, &m); err != nil {
		return err
	}
	emailID, err := reporter.SendMessage(ctx, "subpool-reset-batch-"+b.ID, m)
	if err != nil {
		return err
	}
	if err := st.FinishResetReport(ctx, b.ID, emailID); err != nil {
		return err
	}
	slog.Info("automatic reset batch reported", "batch_id", b.ID, "email_id", emailID, "accounts", len(b.Jobs))
	return nil
}

func (s *Server) runResetJob(ctx context.Context, st autoResetStore, j *store.ResetJob, allowNew bool) error {
	recovering := j.AttemptedAt.Valid
	j.Outcome, j.Detail = "failed", "Pre-reset verification failed; no reset request was sent. Please check SubPool manually."
	if recovering {
		j.Outcome, j.Detail = "unconfirmed", "An earlier request may have been submitted; no second request was sent. Please check manually."
	}
	if !recovering && !allowNew {
		j.Detail = "The awake execution window was missed; no reset request was sent. Please check manually."
		return st.FinishResetJob(ctx, *j)
	}
	account, credentials, err := s.codexSubscriptionCredentials(ctx, j.AccountID)
	if err != nil {
		return st.FinishResetJob(ctx, *j)
	}
	if account.Status == domain.AccountDisabled {
		j.Outcome, j.Detail = "skipped", "Account is disabled; no reset request was sent."
		return st.FinishResetJob(ctx, *j)
	}
	readCtx, readCancel := context.WithTimeout(ctx, 25*time.Second)
	credits, err := s.resets.ReadResetCredits(readCtx, credentials)
	readCancel()
	if err != nil {
		return st.FinishResetJob(ctx, *j)
	}
	var selected *codex.ResetCredit
	if credits != nil {
		for _, c := range credits.Credits {
			if c.ID == j.CreditID {
				copy := c
				selected = &copy
				break
			}
		}
	}
	if s.health != nil {
		fresh, healthErr := s.health.CheckAccount(ctx, j.AccountID)
		if healthErr == nil {
			account = fresh
		}
	}
	if recovering {
		j.After = account.QuotaSnapshot
		if selected != nil && selected.Status == "redeemed" {
			j.Outcome, j.Detail = "succeeded", "The upstream confirms that the selected credit was redeemed."
		}
		return st.FinishResetJob(ctx, *j)
	}
	j.Before = account.QuotaSnapshot
	if selected == nil || selected.Status != "available" || selected.ExpiresAt == nil || *selected.ExpiresAt <= time.Now().Unix() {
		j.Detail = "The selected credit is unavailable or expired. No alternative credit was used."
		return st.FinishResetJob(ctx, *j)
	}
	if resetCapacityFull(j.Before) {
		j.Outcome, j.Detail, j.After = "skipped", "All reported rate-limit windows are full; no credit was used.", j.Before
		return st.FinishResetJob(ctx, *j)
	}
	if err := st.StartResetAttempt(ctx, *j); err != nil {
		return err
	}
	j.Outcome, j.Detail = "unconfirmed", "The reset request could not be confirmed. Check SubPool before using another credit."
	sum := sha256.Sum256([]byte(j.AccountID + "\n" + j.CreditID))
	sum[6] = (sum[6] & 15) | 64
	sum[8] = (sum[8] & 63) | 128
	key := fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	consumeCtx, consumeCancel := context.WithTimeout(ctx, 35*time.Second)
	result, consumeErr := s.resets.ConsumeResetCredit(consumeCtx, credentials, j.CreditID, key)
	consumeCancel()
	if consumeErr == nil {
		switch result.Outcome {
		case "reset", "alreadyRedeemed":
			j.Outcome, j.Detail = "succeeded", "The upstream confirmed redemption."
			s.persistResetQuota(ctx, account, result.QuotaSnapshot)
		case "nothingToReset", "noCredits":
			j.Outcome, j.Detail = "not_applied", "The upstream did not apply a reset. Please check manually."
		}
		if result.ResetCredits != nil {
			if raw, encodeErr := json.Marshal(result.ResetCredits); encodeErr == nil {
				_ = s.store.SetProviderResetCredits(ctx, j.AccountID, raw, time.Now().UTC())
			}
		}
	}
	if s.health != nil {
		_, _ = s.health.CheckAccount(ctx, j.AccountID)
	}
	if after, readErr := s.store.GetProviderAccount(ctx, j.AccountID); readErr == nil {
		j.After = after.QuotaSnapshot
	}
	if j.Outcome == "unconfirmed" {
		checkCtx, checkCancel := context.WithTimeout(ctx, 20*time.Second)
		if fresh, checkErr := s.resets.ReadResetCredits(checkCtx, credentials); checkErr == nil && fresh != nil {
			for _, c := range fresh.Credits {
				if c.ID == j.CreditID && c.Status == "redeemed" {
					j.Outcome, j.Detail = "succeeded", "Redemption was confirmed by a follow-up status read."
				}
			}
		}
		checkCancel()
	}
	_ = s.store.Audit(ctx, domain.AuditEvent{Actor: "auto-reset", Action: "provider_account.reset_credit.consume", TargetType: "provider_account", TargetID: j.AccountID, Result: j.Outcome})
	return st.FinishResetJob(ctx, *j)
}

func resetCapacity(raw []byte) string {
	var q codex.UsageSnapshot
	if json.Unmarshal(raw, &q) != nil || q.Weekly == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.1f%%", q.Weekly.RemainingPercent)
}

func resetCapacityFull(raw []byte) bool {
	var q codex.UsageSnapshot
	if json.Unmarshal(raw, &q) != nil {
		return false
	}
	seen := false
	for _, w := range []*codex.UsageWindow{q.FiveHour, q.Weekly} {
		if w != nil {
			seen = true
			if w.RemainingPercent < 100 {
				return false
			}
		}
	}
	return seen
}
