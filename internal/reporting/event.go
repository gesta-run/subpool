package reporting

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"time"

	"github.com/gesta-run/subpool/internal/reporting/modelusage"
)

func (s *Service) fillModelUsage(ctx context.Context, r *Report, start, end time.Time) {
	source := s.ModelUsage
	if source == nil && s.Config.GestaUsageURL != "" {
		source = &modelusage.Client{URL: s.Config.GestaUsageURL, Token: s.Config.GestaUsageToken}
	}
	if source == nil {
		return
	}
	r.ModelUsage = modelusage.View{Enabled: true, Unavailable: true}
	snapshot, err := source.Read(ctx, start, end)
	if err != nil {
		slog.Warn("employee model report unavailable")
		return
	}
	employees := make([]modelusage.Employee, 0, len(snapshot.Employees))
	for _, employee := range snapshot.Employees {
		if s.Config.People.Hidden(employee.ID, employee.Email, employee.Name) {
			continue
		}
		employee.Name = s.Config.People.Name(employee.Name, employee.Email, employee.ID)
		employees = append(employees, employee)
	}
	snapshot.Employees = employees
	r.ModelUsage = modelusage.Build(snapshot, number)
	r.ModelUsage.Window = start.In(s.Config.Location).Format("15:04") + "–" + end.In(s.Config.Location).Format("15:04")
}

// EventMessage reuses the live daily report queries and template for an event email.
func (s *Service) EventMessage(ctx context.Context, now time.Time, title, notice, recipient string, results ...ResetResult) Message {
	c := s.Config
	c.To = []string{recipient}
	local := now.In(c.Location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.Location)
	modelStart := start
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
				s.fillModelUsage(ctx, &r, modelStart, now)
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
