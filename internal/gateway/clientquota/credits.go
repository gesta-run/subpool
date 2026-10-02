package clientquota

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

const (
	hasCreditsHeader = "X-Codex-Credits-Has-Credits"
	unlimitedHeader  = "X-Codex-Credits-Unlimited"
	balanceHeader    = "X-Codex-Credits-Balance"
	maxSnapshotAge   = 15 * time.Minute
)

type Snapshot struct {
	HasCredits bool
	Unlimited  bool
	Balance    string
}

func FromAccount(account domain.ProviderAccount, now time.Time) (Snapshot, bool) {
	if account.Provider != domain.ProviderCopilot || len(account.QuotaSnapshot) == 0 ||
		account.QuotaCheckedAt == nil || account.LastQuotaErrorCode != "" || now.Sub(*account.QuotaCheckedAt) > maxSnapshotAge {
		return Snapshot{}, false
	}
	var quota copilot.CreditsSnapshot
	if json.Unmarshal(account.QuotaSnapshot, &quota) != nil || quota.Credits == nil {
		return Snapshot{}, false
	}
	credits := quota.Credits
	result := Snapshot{Unlimited: credits.Unlimited}
	if credits.Remaining > 0 && !math.IsNaN(credits.Remaining) && !math.IsInf(credits.Remaining, 0) {
		result.HasCredits = true
		result.Balance = strconv.FormatFloat(credits.Remaining, 'f', -1, 64)
	}
	return result, true
}

func ApplyHeaders(header http.Header, account domain.ProviderAccount, now time.Time) {
	credits, ok := FromAccount(account, now)
	if !ok {
		return
	}
	header.Set(hasCreditsHeader, strconv.FormatBool(credits.HasCredits))
	header.Set(unlimitedHeader, strconv.FormatBool(credits.Unlimited))
	if credits.Balance == "" {
		header.Del(balanceHeader)
		return
	}
	header.Set(balanceHeader, credits.Balance)
}

func CopyHeaders(dst, src http.Header) {
	for _, name := range []string{hasCreditsHeader, unlimitedHeader, balanceHeader} {
		if value := src.Get(name); value != "" {
			dst.Set(name, value)
		}
	}
}

func EventFromHeaders(header http.Header, streamID string) []byte {
	hasCredits, hasCreditsErr := strconv.ParseBool(header.Get(hasCreditsHeader))
	unlimited, unlimitedErr := strconv.ParseBool(header.Get(unlimitedHeader))
	if hasCreditsErr != nil || unlimitedErr != nil {
		return nil
	}
	event := map[string]any{
		"type":               "codex.rate_limits",
		"metered_limit_name": "codex",
		"credits": map[string]any{
			"has_credits": hasCredits,
			"unlimited":   unlimited,
			"balance":     header.Get(balanceHeader),
		},
	}
	if streamID != "" {
		event["stream_id"] = streamID
	}
	payload, _ := json.Marshal(event)
	return payload
}
