package resetnotice

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	Enabled                           bool
	URL, Authorization, Tool, Channel string
}

func LoadConfig() (Config, error) {
	c := Config{URL: strings.TrimSpace(os.Getenv("SUBPOOL_RESET_NOTICE_MCP_URL")), Authorization: strings.TrimSpace(os.Getenv("SUBPOOL_RESET_NOTICE_MCP_AUTHORIZATION")), Tool: strings.TrimSpace(os.Getenv("SUBPOOL_RESET_NOTICE_MCP_TOOL")), Channel: strings.TrimSpace(os.Getenv("SUBPOOL_RESET_NOTICE_CHANNEL"))}
	var err error
	if value := os.Getenv("SUBPOOL_RESET_NOTICE_ENABLED"); value != "" {
		c.Enabled, err = strconv.ParseBool(value)
		if err != nil {
			return c, fmt.Errorf("invalid SUBPOOL_RESET_NOTICE_ENABLED")
		}
	}
	if c.Tool == "" {
		c.Tool = "slack__slack_send_message"
	}
	if !c.Enabled {
		return c, nil
	}
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || c.Authorization == "" || c.Channel == "" {
		return c, fmt.Errorf("reset notices require an HTTPS MCP URL, authorization, and Slack channel")
	}
	return c, nil
}

type Event struct {
	Account, Kind string
	At            time.Time
}

type Store interface {
	ResetNoticeEvents(context.Context, time.Time, time.Time) ([]Event, error)
	ClaimResetNotice(context.Context, string, string, string) (bool, error)
	FinishResetNotice(context.Context, string, string, string, string) error
}

type Sender interface {
	Send(context.Context, string, string) (string, error)
}

type Service struct {
	Config Config
	Store  Store
	Sender Sender
}

func (s *Service) Run(ctx context.Context) {
	if !s.Config.Enabled {
		return
	}
	slog.Info("Codex reset Slack notices enabled", "timezone", "Asia/Shanghai", "schedule", "weekdays 09:00")
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		cycle, cancel := context.WithTimeout(ctx, 45*time.Second)
		if err := s.SendDue(cycle, time.Now()); err != nil {
			slog.Error("Codex reset Slack notice failed; check delivery before retrying", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) SendDue(ctx context.Context, now time.Time) error {
	if !s.Config.Enabled {
		return nil
	}
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return err
	}
	local := now.In(zone)
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday || local.Hour() != 9 || local.Minute() != 0 {
		return nil
	}
	end := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, zone)
	events, err := s.Store.ResetNoticeEvents(ctx, now, end)
	if err != nil || len(events) == 0 {
		return err
	}
	message := Message(local, events)
	day := local.Format("2006-01-02")
	claimed, err := s.Store.ClaimResetNotice(ctx, day, s.Config.Channel, message)
	if err != nil || !claimed {
		return err
	}
	// The MCP tool has no idempotency parameter. Persist the attempt first and
	// never automatically repost an uncertain result after a timeout or restart.
	link, sendErr := s.Sender.Send(ctx, s.Config.Channel, message)
	result := "sent"
	if sendErr != nil {
		result = "unconfirmed"
	}
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.Store.FinishResetNotice(finish, day, s.Config.Channel, result, link); err != nil {
		return err
	}
	return sendErr
}

func Message(now time.Time, events []Event) string {
	zone, _ := time.LoadLocation("Asia/Shanghai")
	var b strings.Builder
	fmt.Fprintf(&b, "@channel Codex quota is scheduled to reset today, %s (Beijing time, UTC+8):\n", now.In(zone).Format("January 2, 2006"))
	for _, e := range events {
		fmt.Fprintf(&b, "• %s — **%s** (%s)\n", e.Account, e.At.In(zone).Format("15:04"), e.Kind)
	}
	b.WriteString("\nIf you have tasks that benefit from advanced models, feel free to run them through SubPool before the reset!")
	return b.String()
}
