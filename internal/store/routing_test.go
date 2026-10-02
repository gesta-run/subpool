package store

import (
	"bytes"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestRouteAccountPreservesQuotaMetadata(t *testing.T) {
	checkedAt := time.Date(2026, 10, 2, 12, 34, 56, 0, time.UTC)
	quota := []byte(`{"credits":{"remaining":499}}`)
	account := routeAccount(
		"account-id", "copilot", "subscription_oauth", "Copilot", []byte("encrypted"), 1,
		"active", false, "healthy", quota, pgtype.Timestamptz{Time: checkedAt, Valid: true},
		"quota_probe_rate_limited", pgtype.Timestamptz{}, pgtype.Timestamptz{}, pgtype.Timestamptz{},
		pgtype.Timestamptz{Time: checkedAt, Valid: true}, pgtype.Timestamptz{Time: checkedAt, Valid: true},
	)

	if !bytes.Equal(account.QuotaSnapshot, quota) || account.QuotaCheckedAt == nil ||
		!account.QuotaCheckedAt.Equal(checkedAt) || account.LastQuotaErrorCode != "quota_probe_rate_limited" {
		t.Fatalf("quota metadata = snapshot %s, checked at %v, error %q", account.QuotaSnapshot, account.QuotaCheckedAt, account.LastQuotaErrorCode)
	}
}
