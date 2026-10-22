package reporting

import (
	"context"
	"fmt"
	"html"
	"time"
)

// EventMessage reuses the live daily report queries and template for an event email.
func (s *Service) EventMessage(ctx context.Context, now time.Time, title, notice, recipient string, results ...ResetResult) Message {
	c := s.Config
	c.To = []string{recipient}
	local := now.In(c.Location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.Location)
	tracking, err := s.Store.ReportTrackingStarted(ctx)
	partial := err == nil && tracking.After(start)
	if partial {
		start = tracking
	}
	if err == nil {
		accounts, readErr := s.Store.ReportAccounts(ctx, start, now)
		if readErr == nil {
			unattributed, countErr := s.Store.ReportUnattributedRequests(ctx, start, now)
			if countErr == nil {
				r := NewReport(c, accounts, start, now, now, partial, unattributed)
				r.NoticeTitle, r.Notice = title, notice
				r.ResetResults = results
				m, renderErr := Render(c, r)
				if renderErr == nil {
					m.Subject = "SubPool · " + title + " · " + local.Format("2006-01-02 15:04")
					return m
				}
			}
		}
	}
	return Message{From: c.From, To: c.To, Subject: "SubPool · " + title,
		HTML: fmt.Sprintf(`<!doctype html><html><body style="font-family:Arial;padding:28px;color:#151922"><h1>SubPool</h1><h2>%s</h2><p style="white-space:pre-line">%s</p><p>Usage snapshot unavailable. Please check the dashboard.</p><a href="%s">Open SubPool</a></body></html>`, html.EscapeString(title), html.EscapeString(notice), html.EscapeString(c.PublicURL)),
		Text: title + "\n" + notice + "\nUsage snapshot unavailable; please check SubPool."}
}
