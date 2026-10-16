package reporting

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

type Account struct {
	ID, Name  string
	Requests  int64
	Snapshot  []byte
	CheckedAt time.Time
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
		name := a.Name
		if strings.EqualFold(name, "codex1") {
			name = "Codex 1"
		}
		if name == "codex-2" {
			name = "Codex 2"
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
			row.Reset = "Resets " + time.Unix(quota.Weekly.Reset, 0).In(c.Location).Format("Jan 2 · 15:04")
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
