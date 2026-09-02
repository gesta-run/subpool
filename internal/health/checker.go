package health

import (
	"context"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"github.com/gesta-run/subpool/internal/store"
)

const (
	checkInterval = 5 * time.Minute
	checkTimeout  = 5 * time.Second
)

type Cipher interface {
	Decrypt([]byte) ([]byte, error)
}

type CodexUsage interface {
	Usage(context.Context, codex.Credentials) (codex.UsageSnapshot, error)
}

type CompatibleModels interface {
	Models(context.Context, openaicompat.Credentials) (*http.Response, error)
}

type Result struct {
	HealthStatus  string
	ErrorCode     string
	Email         string
	QuotaSnapshot json.RawMessage
	AuthFailed    bool
	Failure       bool
}

type Checker struct {
	store      store.Store
	cipher     Cipher
	codex      CodexUsage
	compatible CompatibleModels
	now        func() time.Time
}

func NewChecker(st store.Store, cipher Cipher, codexUsage CodexUsage, compatible CompatibleModels) *Checker {
	return &Checker{store: st, cipher: cipher, codex: codexUsage, compatible: compatible, now: time.Now}
}

func (c *Checker) Check(ctx context.Context, account domain.ProviderAccount) Result {
	plaintext, err := c.cipher.Decrypt(account.CredentialCiphertext)
	if err != nil {
		return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "credential_unavailable", Failure: true}
	}
	switch account.Provider {
	case domain.ProviderCodex:
		var credentials codex.Credentials
		if json.Unmarshal(plaintext, &credentials) != nil {
			return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "credential_unavailable", Failure: true}
		}
		snapshot, usageErr := c.codex.Usage(ctx, credentials)
		err = usageErr
		if err == nil {
			raw, marshalErr := json.Marshal(snapshot)
			if marshalErr != nil {
				return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "invalid_usage_response", Failure: true}
			}
			return Result{HealthStatus: domain.HealthHealthy, Email: credentials.Email, QuotaSnapshot: raw}
		}
		return classifyError(err)
	case domain.ProviderOpenAICompatible:
		var credentials openaicompat.Credentials
		if json.Unmarshal(plaintext, &credentials) != nil {
			return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "credential_unavailable", Failure: true}
		}
		resp, requestErr := c.compatible.Models(ctx, credentials)
		if requestErr != nil {
			return classifyError(requestErr)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return Result{HealthStatus: domain.HealthHealthy}
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "authentication_failed", AuthFailed: true}
		case resp.StatusCode == http.StatusTooManyRequests:
			return Result{HealthStatus: domain.HealthHealthy}
		case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
			return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "probe_unsupported"}
		case resp.StatusCode >= 500:
			return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_5xx", Failure: true}
		default:
			return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_error", Failure: true}
		}
	default:
		return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "probe_unsupported"}
	}
}

func (c *Checker) ApplyNewAccount(account *domain.ProviderAccount, result Result) {
	now := c.now()
	next := c.nextCheck(account.ID, now)
	account.HealthStatus = result.HealthStatus
	account.Email = result.Email
	account.QuotaSnapshot = result.QuotaSnapshot
	account.LastHealthErrorCode = result.ErrorCode
	account.LastCheckedAt = &now
	account.NextHealthCheckAt = &next
	if result.Failure {
		account.ConsecutiveFailures = 1
	}
}

func (c *Checker) CheckAccount(ctx context.Context, accountID string) (domain.ProviderAccount, error) {
	account, err := c.store.GetProviderAccount(ctx, accountID)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	result := c.Check(ctx, account)
	persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = c.persist(persistCtx, account.ID, result); err != nil {
		return domain.ProviderAccount{}, err
	}
	return c.store.GetProviderAccount(persistCtx, account.ID)
}

func (c *Checker) Run(ctx context.Context) {
	c.runBatch(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.runBatch(ctx)
		}
	}
}

func (c *Checker) runBatch(ctx context.Context) {
	now := c.now()
	accounts, err := c.store.ClaimProviderHealthChecks(ctx, 50, now, now.Add(2*time.Minute))
	if err != nil {
		slog.Error("provider health claim failed", "error", err)
		return
	}
	if len(accounts) > 0 {
		slog.Info("provider health batch claimed", "accounts", len(accounts))
	}
	semaphore := make(chan struct{}, 8)
	var wait sync.WaitGroup
	for _, account := range accounts {
		account := account
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
			result := c.Check(checkCtx, account)
			cancel()
			persistCtx, persistCancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer persistCancel()
			if persistErr := c.persist(persistCtx, account.ID, result); persistErr != nil && !errors.Is(persistErr, context.Canceled) {
				slog.Error("provider health update failed", "account_id", account.ID, "error", persistErr)
			}
		}()
	}
	wait.Wait()
}

func (c *Checker) persist(ctx context.Context, accountID string, result Result) error {
	now := c.now()
	next := c.nextCheck(accountID, now)
	var err error
	if result.AuthFailed {
		err = c.store.UpdateProviderStatus(ctx, accountID, domain.AccountAuthFailed, nil)
	} else if result.Failure {
		err = c.store.RecordProviderHealthFailure(ctx, accountID, result.ErrorCode, now, next)
	} else {
		err = c.store.SetProviderHealth(ctx, accountID, result.HealthStatus, result.ErrorCode, now, next)
	}
	if err != nil {
		return err
	}
	if result.Email != "" || len(result.QuotaSnapshot) > 0 {
		return c.store.UpdateProviderDetails(ctx, accountID, result.Email, result.QuotaSnapshot)
	}
	return nil
}

func (c *Checker) nextCheck(accountID string, now time.Time) time.Time {
	jitter := time.Duration(crc32.ChecksumIEEE([]byte(accountID))%60) * time.Second
	return now.Add(checkInterval + jitter)
}

func classifyError(err error) Result {
	var statusErr *codex.HTTPStatusError
	if errors.As(err, &statusErr) {
		switch {
		case statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden:
			return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "authentication_failed", AuthFailed: true}
		case statusErr.StatusCode == http.StatusTooManyRequests:
			return Result{HealthStatus: domain.HealthHealthy}
		case statusErr.StatusCode == http.StatusNotFound || statusErr.StatusCode == http.StatusMethodNotAllowed:
			return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "probe_unsupported"}
		case statusErr.StatusCode >= 500:
			return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_5xx", Failure: true}
		default:
			return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "provider_error", Failure: true}
		}
	}
	message := strings.ToLower(err.Error())
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(message, "timeout") {
		return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "timeout", Failure: true}
	}
	if strings.Contains(message, "status 401") || strings.Contains(message, "status 403") {
		return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "authentication_failed", AuthFailed: true}
	}
	return Result{HealthStatus: domain.HealthUnknown, ErrorCode: "connection_failed", Failure: true}
}
