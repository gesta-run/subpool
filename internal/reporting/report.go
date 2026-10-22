package reporting

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type Employee struct {
	Name     string `json:"name"`
	Requests int64  `json:"requests"`
}

type EmployeeRow struct {
	Name, Requests, Share string
}

type Account struct {
	ID, Name, Email string
	Employees       []Employee
	Requests        int64
	Snapshot        []byte
	CheckedAt       time.Time
}
type Row struct {
	Employees                               []EmployeeRow
	Name, Requests, Share, Remaining, Reset string
	Bar                                     int
}
type Report struct {
	Date, Time, Zone, Window, URL string
	NoticeTitle, Notice           string
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
			row.Employees = append(row.Employees, EmployeeRow{Name: employee.Name, Requests: number(employee.Requests), Share: share})
		}
		fillQuota(&row, a, now, c.Location)
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
	return fmt.Sprintf("Resets %s (in %d %s)", reset.In(location).Format("2006-01-02 · 15:04"), count, unit)
}

type quotaWindow struct {
	Remaining *float64 `json:"remaining_percent"`
	Reset     int64    `json:"reset_at"`
}

func fillQuota(row *Row, a Account, now time.Time, location *time.Location) {
	if a.CheckedAt.IsZero() || a.CheckedAt.After(now.Add(time.Minute)) || now.Sub(a.CheckedAt) > 15*time.Minute {
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
