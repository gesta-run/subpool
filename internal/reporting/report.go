package reporting

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"github.com/gesta-run/subpool/internal/reporting/modelusage"
)

type Employee struct {
	Name     string `json:"name"`
	Requests int64  `json:"requests"`
}

type EmployeeRow struct {
	Name, Requests, Share string
}

type Account struct {
	ID, Name, Email          string
	QuotaError               string
	Provider, CredentialType string
	Employees                []Employee
	Requests                 int64
	Snapshot                 []byte
	CheckedAt                time.Time
	ResetSnapshot            []byte
	ResetCheckedAt           time.Time
}
type Row struct {
	BalanceEstimated                        bool
	Employees                               []EmployeeRow
	Name, Requests, Share, Remaining, Reset string
	Bar                                     int
	AICredits, ResetCredits, CreditExpiry   string
	ExpirySoon                              bool
	HideBar                                 bool
	BalanceUpdated, BalanceStatus           string
}
type ResetResult struct {
	Account, Outcome, Before, After, Detail string
}
type Report struct {
	ModelUsage                    modelusage.View
	Date, Time, Zone, Window, URL string
	NoticeTitle, Notice           string
	ResetResults                  []ResetResult
	Rows                          []Row
	Total                         int64
	Unattributed                  int64
	UnattributedShare             string
	Partial                       bool
}
type Message struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

func NewReport(c Config, accounts []Account, start, end, now time.Time, partial bool, unattributed int64) Report {
	r := Report{Date: end.In(c.Location).Format("January 2, 2006"), Time: end.In(c.Location).Format("15:04"), Zone: c.Location.String(), Window: start.In(c.Location).Format("15:04") + "–" + end.In(c.Location).Format("15:04"), URL: c.PublicURL, Partial: partial, Total: unattributed, Unattributed: unattributed}
	for _, a := range accounts {
		r.Total += a.Requests
	}
	if r.Total > 0 {
		r.UnattributedShare = fmt.Sprintf("%.1f%%", float64(r.Unattributed)*100/float64(r.Total))
	}
	for _, a := range accounts {
		name := strings.TrimSpace(a.Email)
		if name == "" {
			name = a.Name
		}
		row := Row{Name: name, Requests: number(a.Requests), Share: "0.0%", Remaining: "Unavailable", Reset: "Quota refresh unavailable"}
		if r.Total > 0 {
			row.Share = fmt.Sprintf("%.1f%%", float64(a.Requests)*100/float64(r.Total))
		}
		for _, employee := range a.Employees {
			share := "0.0%"
			if a.Requests > 0 {
				share = fmt.Sprintf("%.1f%%", float64(employee.Requests)*100/float64(a.Requests))
			}
			row.Employees = append(row.Employees, EmployeeRow{Name: c.People.Name(employee.Name), Requests: number(employee.Requests), Share: share})
		}
		fillQuota(&row, a, now, c.Location)
		fillCredits(&row, a, now, c.Location)
		r.Rows = append(r.Rows, row)
	}
	return r
}

func number(n int64) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func resetLabel(reset, now time.Time, location *time.Location) string {
	return timeLabel("Resets", reset, now, location)
}

func timeLabel(prefix string, reset, now time.Time, location *time.Location) string {
	remaining := reset.Sub(now)
	unit, duration := "minute", time.Minute
	if remaining >= 24*time.Hour {
		unit, duration = "day", 24*time.Hour
	} else if remaining >= time.Hour {
		unit, duration = "hour", time.Hour
	}
	count := int(math.Ceil(float64(remaining) / float64(duration)))
	if count != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%s %s (in %d %s)", prefix, reset.In(location).Format("2006-01-02 · 15:04"), count, unit)
}

func freshQuota(checked, now time.Time) bool {
	return !checked.IsZero() && !checked.After(now.Add(time.Minute)) && now.Sub(checked) <= 15*time.Minute
}

func fillCredits(row *Row, a Account, now time.Time, location *time.Location) {
	if a.Provider != "codex" || a.CredentialType == "api_key" {
		return
	}
	row.AICredits, row.ResetCredits = "Unavailable", "Unavailable"
	var quota struct {
		Credits *struct {
			Balance   *string `json:"balance"`
			Unlimited bool    `json:"unlimited"`
		} `json:"credits"`
	}
	if freshQuota(a.CheckedAt, now) && json.Unmarshal(a.Snapshot, &quota) == nil && quota.Credits != nil {
		if quota.Credits.Unlimited {
			row.AICredits = "Unlimited"
		} else if quota.Credits.Balance != nil {
			if value, err := strconv.ParseFloat(strings.TrimSpace(*quota.Credits.Balance), 64); err == nil && !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value < math.MaxInt64 {
				row.AICredits = number(int64(math.Round(value)))
			}
		}
	}
	var resets struct {
		Available *int `json:"available_count"`
		Credits   []struct {
			Status    string `json:"status"`
			ExpiresAt *int64 `json:"expires_at"`
		} `json:"credits"`
	}
	if !freshQuota(a.ResetCheckedAt, now) || json.Unmarshal(a.ResetSnapshot, &resets) != nil || resets.Available == nil || *resets.Available < 0 {
		return
	}
	// Keep the provider's available count, including credits without an expiry.
	available := *resets.Available
	var next int64
	for _, credit := range resets.Credits {
		if credit.Status != "available" || credit.ExpiresAt == nil || *credit.ExpiresAt <= 0 {
			continue
		}
		if *credit.ExpiresAt <= now.Unix() {
			continue
		}
		if next == 0 || *credit.ExpiresAt < next {
			next = *credit.ExpiresAt
		}
	}
	row.ResetCredits = number(int64(available))
	if available > 0 {
		row.CreditExpiry = "Reset expiry unavailable"
		if next > 0 {
			expiry := time.Unix(next, 0)
			row.CreditExpiry = timeLabel("Next reset expires", expiry, now, location)
			row.ExpirySoon = expiry.Sub(now) <= 24*time.Hour
		}
	}
}

type quotaWindow struct {
	Remaining *float64 `json:"remaining_percent"`
	Reset     int64    `json:"reset_at"`
}

func fillQuota(row *Row, a Account, now time.Time, location *time.Location) {
	if fillOpenAIBalance(row, a, now, location) {
		return
	}
	if !freshQuota(a.CheckedAt, now) {
		return
	}
	var quota struct {
		Weekly  *quotaWindow `json:"weekly"`
		Monthly *quotaWindow `json:"monthly"`
		Credits *struct {
			quotaWindow
			Remaining   *int64 `json:"remaining"`
			Entitlement *int64 `json:"entitlement"`
		} `json:"credits"`
	}
	if json.Unmarshal(a.Snapshot, &quota) != nil {
		return
	}
	window, kind := quota.Weekly, "Weekly"
	if window == nil {
		window, kind = quota.Monthly, "Monthly"
	}
	if window == nil && quota.Credits != nil {
		window, kind = &quota.Credits.quotaWindow, "Credits"
	}
	if window == nil || window.Remaining == nil || window.Reset <= now.Unix() || *window.Remaining < 0 || *window.Remaining > 100 {
		return
	}
	if kind == "Credits" && quota.Credits.Remaining != nil && quota.Credits.Entitlement != nil && (*quota.Credits.Remaining < 0 || *quota.Credits.Entitlement <= 0) {
		return
	}
	row.Remaining = fmt.Sprintf("%.0f%%", *window.Remaining)
	if kind == "Monthly" {
		row.Remaining = fmt.Sprintf("%.1f%%", *window.Remaining)
	}
	if kind == "Credits" && quota.Credits.Remaining != nil && quota.Credits.Entitlement != nil {
		row.Remaining = number(*quota.Credits.Remaining) + " / " + number(*quota.Credits.Entitlement)
	}
	row.Bar = int(math.Round(*window.Remaining))
	row.Reset = kind + " · " + resetLabel(time.Unix(window.Reset, 0), now, location)
}

func fillOpenAIBalance(row *Row, a Account, now time.Time, location *time.Location) bool {
	var snapshot struct {
		PlanType string `json:"plan_type"`
		Balance  *struct {
			Remaining    *float64 `json:"remaining_usd"`
			ExpiresAt    int64    `json:"expires_at"`
			Estimated    bool     `json:"estimated"`
			UsageThrough int64    `json:"usage_through"`
		} `json:"balance"`
	}
	if json.Unmarshal(a.Snapshot, &snapshot) != nil || snapshot.PlanType != openaibilling.PlanType {
		return false
	}
	row.HideBar = true
	if snapshot.Balance == nil || snapshot.Balance.Remaining == nil || a.CheckedAt.IsZero() || a.CheckedAt.After(now.Add(time.Minute)) {
		return true
	}
	row.Remaining = fmt.Sprintf("$%.2f", *snapshot.Balance.Remaining)
	row.BalanceEstimated = snapshot.Balance.Estimated
	row.Reset = "Credit expiry not reported"
	if snapshot.Balance.ExpiresAt > 0 {
		expiry := time.Unix(snapshot.Balance.ExpiresAt, 0)
		if expiry.After(now) {
			row.Reset = timeLabel("Credits expire", expiry, now, location)
		} else {
			row.Reset = "Credits expired " + expiry.In(location).Format("2006-01-02 · 15:04")
		}
	}
	row.BalanceUpdated = "Updated " + a.CheckedAt.In(location).Format("2006-01-02 · 15:04")
	if snapshot.Balance.Estimated {
		row.BalanceUpdated = "Estimated · " + row.BalanceUpdated
		if snapshot.Balance.UsageThrough > 0 {
			row.BalanceUpdated += " · Costs through " + time.Unix(snapshot.Balance.UsageThrough, 0).In(location).Format("2006-01-02 · 15:04")
		}
	}
	fresh := freshQuota(a.CheckedAt, now)
	if snapshot.Balance.Estimated {
		fresh = !a.CheckedAt.After(now.Add(time.Minute)) && now.Sub(a.CheckedAt) <= openaibilling.SnapshotFreshFor
	}
	if !fresh || a.QuotaError != "" {
		row.BalanceStatus = "Last known balance; refresh unavailable"
		if a.QuotaError == "quota_probe_rate_limited" {
			row.BalanceStatus = "Refresh delayed by OpenAI rate limit; showing last estimate"
		} else if a.QuotaError == "" {
			row.BalanceStatus = "Last known balance; update pending"
		}
		if a.QuotaError == "quota_authorization_expired" || a.QuotaError == "quota_authorization_required" {
			row.BalanceStatus = "Last known balance; reconnect billing"
		}
	}
	return true
}
