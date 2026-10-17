package reporting

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type Account struct {
	ID, Name, Email string
	Requests        int64
	Snapshot        []byte
	CheckedAt       time.Time
}
type Row struct {
	Name, Requests, Share, Remaining, Reset string
	Bar                                     int
}
type Report struct {
	Date, Time, Zone, Window, URL string
	Rows                          []Row
	Total                         int64
	Partial                       bool
}
type Message struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

func NewReport(c Config, accounts []Account, start, end, now time.Time, partial bool) Report {
	r := Report{Date: end.In(c.Location).Format("January 2, 2006"), Time: end.In(c.Location).Format("15:04"), Zone: c.Location.String(), Window: start.In(c.Location).Format("15:04") + "–" + end.In(c.Location).Format("15:04"), URL: c.PublicURL, Partial: partial}
	for _, a := range accounts {
		r.Total += a.Requests
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
		var quota struct {
			Weekly *struct {
				Remaining *float64 `json:"remaining_percent"`
				Reset     int64    `json:"reset_at"`
			} `json:"weekly"`
		}
		if json.Unmarshal(a.Snapshot, &quota) == nil && quota.Weekly != nil && quota.Weekly.Remaining != nil && !a.CheckedAt.IsZero() && !a.CheckedAt.After(now.Add(time.Minute)) && now.Sub(a.CheckedAt) <= 15*time.Minute && quota.Weekly.Reset > now.Unix() && *quota.Weekly.Remaining >= 0 && *quota.Weekly.Remaining <= 100 {
			row.Remaining = fmt.Sprintf("%.0f%%", *quota.Weekly.Remaining)
			row.Bar = int(math.Round(*quota.Weekly.Remaining))
			row.Reset = resetLabel(time.Unix(quota.Weekly.Reset, 0), now, c.Location)
		}
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
