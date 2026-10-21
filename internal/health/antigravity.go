package health

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
)

type AntigravityModels interface {
	Models(context.Context, antigravity.Credentials) (antigravity.Snapshot, error)
}

func (c *Checker) WithAntigravity(client AntigravityModels, refresher credential.AccountRefresher) *Checker {
	c.antigravity, c.refresher = client, refresher
	return c
}

func (c *Checker) checkAntigravity(ctx context.Context, account domain.ProviderAccount, plaintext []byte) Result {
	var credentials antigravity.Credentials
	if c.antigravity == nil || json.Unmarshal(plaintext, &credentials) != nil {
		return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "credential_unavailable", Failure: true}
	}
	var err error
	refreshed := false
	if time.Until(credentials.ExpiresAt) <= time.Minute {
		if c.refresher == nil {
			return antigravityQuotaFailure(errors.New("refresh unavailable"))
		}
		account, err = c.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
		if err != nil {
			return antigravityQuotaFailure(err)
		}
		plaintext, err = c.cipher.Decrypt(account.CredentialCiphertext)
		if err != nil || json.Unmarshal(plaintext, &credentials) != nil {
			return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "credential_unavailable", Failure: true}
		}
		refreshed = true
	}
	snapshot, err := c.antigravity.Models(ctx, credentials)
	if antigravity.IsDefinitiveAuthError(err) && !refreshed && c.refresher != nil {
		account, err = c.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
		if err == nil {
			plaintext, err = c.cipher.Decrypt(account.CredentialCiphertext)
		}
		if err == nil {
			err = json.Unmarshal(plaintext, &credentials)
		}
		if err == nil {
			snapshot, err = c.antigravity.Models(ctx, credentials)
		}
	}
	if err != nil {
		return antigravityQuotaFailure(err)
	}
	// Per-model exhaustion must not disable every model on the account.
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return antigravityQuotaFailure(err)
	}
	return Result{HealthStatus: domain.HealthHealthy, Email: credentials.Email, QuotaSnapshot: raw}
}

func antigravityQuotaFailure(err error) Result {
	if antigravity.IsDefinitiveAuthError(err) {
		return Result{HealthStatus: domain.HealthUnhealthy, ErrorCode: "authentication_failed", QuotaErrorCode: "authentication_failed", AuthFailed: true}
	}
	return Result{HealthStatus: domain.HealthUnknown, QuotaErrorCode: "quota_probe_failed", QuotaProbeFailed: true}
}
