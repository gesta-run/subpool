package reporting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

type Store interface {
	ReportTrackingStarted(context.Context) (time.Time, error)
	ReportAccounts(context.Context, time.Time, time.Time) ([]Account, error)
	ReportUnattributedRequests(context.Context, time.Time, time.Time) (int64, error)
	ClaimDailyReport(context.Context, string, []byte) ([]byte, bool, error)
	FinishDailyReport(context.Context, string, string) error
	RetryDailyReport(context.Context, string) error
}
type Service struct {
	Config   Config
	Store    Store
	Client   *http.Client
	Endpoint string
}

func (s *Service) Run(ctx context.Context) {
	if !s.Config.Enabled {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.SendDue(ctx, time.Now()); err != nil {
			slog.Error("daily team report failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) SendDue(ctx context.Context, now time.Time) error {
	c := s.Config
	if !c.Enabled {
		return nil
	}
	local := now.In(c.Location)
	end := time.Date(local.Year(), local.Month(), local.Day(), c.Hour, c.Minute, 0, 0, c.Location)
	if now.Before(end) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.Location)
	tracking, err := s.Store.ReportTrackingStarted(ctx)
	if err != nil {
		return err
	}
	partial := tracking.After(start)
	if partial {
		start = tracking
	}
	if !start.Before(end) {
		end = now
	}
	accounts, err := s.Store.ReportAccounts(ctx, start, end)
	if err != nil {
		return err
	}
	unattributed, err := s.Store.ReportUnattributedRequests(ctx, start, end)
	if err != nil {
		return err
	}
	report := NewReport(c, accounts, start, end, now, partial, unattributed)
	message, err := Render(c, report)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	day := local.Format("2006-01-02")
	payload, claimed, err := s.Store.ClaimDailyReport(ctx, day, payload)
	if err != nil || !claimed {
		return err
	}
	identity := sha256.Sum256([]byte(c.PublicURL))
	key := fmt.Sprintf("subpool-team-usage-%x-%s", identity[:8], day)
	emailID, err := s.send(ctx, key, payload)
	if err != nil {
		_ = s.Store.RetryDailyReport(ctx, day)
		return err
	}
	if err = s.Store.FinishDailyReport(ctx, day, emailID); err != nil {
		return err
	}
	slog.Info("daily team report accepted", "report_day", day, "email_id", emailID)
	return nil
}

func (s *Service) send(ctx context.Context, key string, payload []byte) (string, error) {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "https://api.resend.com/emails"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+s.Config.APIKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Subpool-Daily-Reports/1.0")
	request.Header.Set("Idempotency-Key", key)
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("send team report: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Resend returned HTTP %d", response.StatusCode)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return "", err
	}
	if result.ID == "" {
		return "", fmt.Errorf("Resend response has no email ID")
	}
	return result.ID, nil
}
