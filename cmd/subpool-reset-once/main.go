// subpool-reset-once redeems one explicitly selected credit and sends one event report.
// It must be invoked by an operator-managed one-shot timer, with a persistent state file.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/reporting"
	"github.com/gesta-run/subpool/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type job struct {
	AccountID    string    `json:"account_id"`
	AccountLabel string    `json:"account_label"`
	CreditID     string    `json:"credit_id"`
	RunAt        time.Time `json:"run_at"`
	Recipient    string    `json:"recipient"`
	Origin       string    `json:"origin"`
}

type account struct {
	ID    string               `json:"id"`
	Email string               `json:"email"`
	Name  string               `json:"display_name"`
	Quota *codex.UsageSnapshot `json:"quota_snapshot"`
}

type state struct {
	Identity    string             `json:"identity"`
	AttemptedAt time.Time          `json:"attempted_at"`
	Outcome     string             `json:"outcome"`
	Detail      string             `json:"detail"`
	Before      *account           `json:"before,omitempty"`
	After       *account           `json:"after,omitempty"`
	Message     *reporting.Message `json:"message,omitempty"`
	EmailID     string             `json:"email_id,omitempty"`
}

type api struct {
	origin string
	client *http.Client
	cookie string
}

func main() {
	jobPath := flag.String("job", "", "Operator-managed job configuration")
	statePath := flag.String("state", "", "Persistent state; never remove to retry a job")
	check := flag.Bool("check", false, "Validate access and render the report without redeeming or sending")
	flag.Parse()
	if err := run(*jobPath, *statePath, *check); err != nil {
		log.Fatal(err)
	}
}

func run(jobPath, statePath string, check bool) error {
	var j job
	if err := readJSON(jobPath, &j); err != nil {
		return err
	}
	u, err := url.Parse(j.Origin)
	if err != nil || u.Scheme != "http" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return errors.New("admin origin must be an HTTP loopback origin")
	}
	address, err := mail.ParseAddress(j.Recipient)
	if err != nil || address.Address != j.Recipient || j.AccountID == "" || j.CreditID == "" || j.RunAt.IsZero() || statePath == "" {
		return errors.New("job requires an account, selected credit, execution time, recipient, and state path")
	}
	c, err := reporting.LoadConfig(os.Getenv("SUBPOOL_PUBLIC_URL"))
	if err != nil {
		return err
	}
	if !c.Enabled {
		return errors.New("existing SubPool report configuration must be enabled")
	}
	c.To = []string{j.Recipient}
	now := time.Now()
	if !check && now.Before(j.RunAt) {
		return errors.New("job is not due; no credit was consumed")
	}
	identity := fmt.Sprintf("%x", sha256.Sum256([]byte(j.AccountID+"\n"+j.CreditID+"\n"+j.RunAt.Format(time.RFC3339)+"\n"+j.Recipient)))
	s := state{Identity: identity}
	if err := readJSON(statePath, &s); err != nil && !os.IsNotExist(err) {
		return err
	}
	if s.Identity != identity {
		return errors.New("state belongs to another job")
	}
	if s.EmailID != "" {
		log.Printf("Job already reported: %s", s.EmailID)
		return nil
	}
	a := &api{origin: j.Origin, client: &http.Client{Timeout: 40 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if check {
		if err := a.login(ctx); err != nil {
			return err
		}
		before, credit, err := a.inspect(ctx, j)
		if err != nil {
			return err
		}
		if credit == nil || credit.Status != "available" || credit.ExpiresAt == nil || !j.RunAt.Before(time.Unix(*credit.ExpiresAt, 0)) {
			return errors.New("selected credit will not be available at the scheduled time")
		}
		if _, err := snapshot(ctx, c, now); err != nil {
			return errors.New("cannot read the usage report database")
		}
		s.Before = before
		s.Outcome = "validation_only"
		if _, err := message(ctx, c, j, s, now); err != nil {
			return err
		}
		log.Printf("Validation passed: account=%s recipient=%s run_at=%s expires_at=%s; no redemption or email", before.Email, j.Recipient, j.RunAt.In(c.Location).Format(time.RFC3339), time.Unix(*credit.ExpiresAt, 0).In(c.Location).Format(time.RFC3339))
		return nil
	}
	if s.Message == nil {
		if err := execute(ctx, a, j, &s, func() error { return writeJSON(statePath, s) }); err != nil {
			return err
		}
		m, err := message(ctx, c, j, s, time.Now())
		if err != nil {
			return err
		}
		s.Message = &m
		if err := writeJSON(statePath, s); err != nil {
			return err
		}
	}
	// Frozen body and stable key allow mail retries without another redemption.
	service := &reporting.Service{Config: c}
	emailID, err := service.SendMessage(ctx, "subpool-reset-once-"+identity[:32], *s.Message)
	if err != nil {
		return err
	}
	s.EmailID = emailID
	if err := writeJSON(statePath, s); err != nil {
		return err
	}
	log.Printf("Reset report accepted: outcome=%s email_id=%s", s.Outcome, emailID)
	return nil
}

func execute(ctx context.Context, a *api, j job, s *state, save func() error) error {
	if s.AttemptedAt.IsZero() {
		return attempt(ctx, a, j, s, save)
	}
	// A previous process may have died after the POST. Never POST again.
	if a.login(ctx) == nil {
		reconcile(ctx, a, j, s)
	}
	return save()
}

func attempt(ctx context.Context, a *api, j job, s *state, save func() error) error {
	s.AttemptedAt = time.Now().UTC()
	s.Outcome, s.Detail = "failed", "Pre-reset verification failed. No reset request was sent; please check SubPool manually."
	if err := a.login(ctx); err != nil {
		return save()
	}
	before, credit, err := a.inspect(ctx, j)
	if err != nil {
		return save()
	}
	s.Before = before
	if credit == nil || credit.Status != "available" || credit.ExpiresAt == nil || *credit.ExpiresAt <= time.Now().Unix() {
		s.Detail = "The selected credit is unavailable or expired. No alternative credit was used."
		return save()
	}
	if full(before.Quota) {
		s.Outcome, s.Detail, s.After = "skipped", "All reported rate-limit windows are already full. The credit was not used.", before
		return save()
	}
	s.Outcome, s.Detail = "unconfirmed", "A reset may have been submitted. Do not spend another credit until its status is checked."
	if err := save(); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(j.AccountID + "\n" + j.CreditID))
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
	var result codex.ConsumeResetCreditResult
	err = a.call(ctx, http.MethodPost, "/api/v1/provider-accounts/"+url.PathEscape(j.AccountID)+"/reset-credits/consume", map[string]string{"credit_id": j.CreditID, "idempotency_key": id}, &result)
	if err == nil {
		switch result.Outcome {
		case "reset", "alreadyRedeemed":
			s.Outcome, s.Detail = "succeeded", "The upstream confirmed that the selected credit was redeemed."
		case "nothingToReset", "noCredits":
			s.Outcome, s.Detail = "not_applied", "The upstream did not apply a reset. Please inspect the selected credit manually."
		}
	}
	reconcile(ctx, a, j, s)
	return save()
}

func reconcile(ctx context.Context, a *api, j job, s *state) {
	// Refresh the quota for the event report; this does not redeem credits.
	_ = a.call(ctx, http.MethodPost, "/api/v1/provider-accounts/"+url.PathEscape(j.AccountID)+"/check", map[string]string{}, nil)
	after, credit, err := a.inspect(ctx, j)
	if err == nil {
		s.After = after
	}
	if s.Outcome == "unconfirmed" && credit != nil && credit.Status == "redeemed" {
		s.Outcome, s.Detail = "succeeded", "The selected credit is confirmed redeemed after reconciliation."
	}
	if s.Outcome == "succeeded" && (err != nil || after == nil || after.Quota == nil) {
		s.Detail += " The refreshed quota is unavailable; use the dashboard to verify capacity."
	}
}

func full(q *codex.UsageSnapshot) bool {
	if q == nil {
		return false
	}
	seen := false
	for _, w := range []*codex.UsageWindow{q.FiveHour, q.Weekly} {
		if w != nil {
			seen = true
			if w.RemainingPercent < 100 {
				return false
			}
		}
	}
	return seen
}

func (a *api) login(ctx context.Context) error {
	return a.call(ctx, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": os.Getenv("SUBPOOL_ADMIN_USERNAME"), "password": os.Getenv("SUBPOOL_ADMIN_PASSWORD")}, nil)
}

func (a *api) inspect(ctx context.Context, j job) (*account, *codex.ResetCredit, error) {
	var responseAccounts struct {
		Data []account `json:"data"`
	}
	if err := a.call(ctx, http.MethodGet, "/api/v1/provider-accounts", nil, &responseAccounts); err != nil {
		return nil, nil, err
	}
	var found *account
	for _, value := range responseAccounts.Data {
		if value.ID == j.AccountID {
			copy := value
			found = &copy
			break
		}
	}
	if found == nil {
		return nil, nil, errors.New("selected account is missing")
	}
	var response struct {
		Credits *codex.ResetCreditsSummary `json:"reset_credits"`
	}
	if err := a.call(ctx, http.MethodGet, "/api/v1/provider-accounts/"+url.PathEscape(j.AccountID)+"/reset-credits?refresh=true", nil, &response); err != nil {
		return found, nil, err
	}
	if response.Credits != nil {
		for _, credit := range response.Credits.Credits {
			if credit.ID == j.CreditID {
				copy := credit
				return found, &copy, nil
			}
		}
	}
	return found, nil, nil
}

func (a *api) call(ctx context.Context, method, path string, body, result any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	r, err := http.NewRequestWithContext(ctx, method, a.origin+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if a.cookie != "" {
		r.Header.Set("Cookie", a.cookie)
	}
	response, err := a.client.Do(r)
	if err != nil {
		return errors.New("SubPool request failed or timed out")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("SubPool returned HTTP %d", response.StatusCode)
	}
	if path == "/api/v1/auth/login" {
		cookies := response.Cookies()
		if len(cookies) == 0 {
			return errors.New("SubPool did not issue an admin session")
		}
		a.cookie = cookies[0].Name + "=" + cookies[0].Value
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result)
	}
	return nil
}

func message(ctx context.Context, c reporting.Config, j job, s state, now time.Time) (reporting.Message, error) {
	name := j.AccountID
	if j.AccountLabel != "" {
		name = j.AccountLabel
	}
	if s.Before != nil && s.Before.Email != "" {
		name = s.Before.Email
	}
	title := "Reset " + s.Outcome + " · " + name
	note := fmt.Sprintf("%s Execution: %s. Weekly capacity: %s → %s. This one-shot job never selects another credit.", s.Detail, s.AttemptedAt.In(c.Location).Format("2006-01-02 15:04 MST"), weekly(s.Before), weekly(s.After))
	r, err := snapshot(ctx, c, now)
	if err != nil {
		// Report failure even when the usage database cannot be read. Never show an invented zero.
		return reporting.Message{From: c.From, To: c.To, Subject: "SubPool · " + title,
			HTML: "<!doctype html><html><body style=\"font-family:Arial;color:#151922;padding:28px\"><h1>SubPool</h1><h2>" + html.EscapeString(title) + "</h2><p>" + html.EscapeString(note) + "</p><p>Usage snapshot unavailable. Please check the dashboard.</p><a href=\"" + html.EscapeString(c.PublicURL) + "\">Open SubPool</a></body></html>",
			Text: title + "\n" + note + "\nUsage snapshot unavailable. Please check SubPool."}, nil
	}
	r.NoticeTitle, r.Notice = title, note
	m, err := reporting.Render(c, r)
	m.Subject = "SubPool · " + title + " · " + now.In(c.Location).Format("2006-01-02 15:04")
	return m, err
}

func weekly(a *account) string {
	if a == nil || a.Quota == nil || a.Quota.Weekly == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.1f%%", a.Quota.Weekly.RemainingPercent)
}

func snapshot(ctx context.Context, c reporting.Config, now time.Time) (reporting.Report, error) {
	pool, err := pgxpool.New(ctx, os.Getenv("SUBPOOL_DATABASE_URL"))
	if err != nil {
		return reporting.Report{}, err
	}
	defer pool.Close()
	queries := storedb.New(pool)
	local := now.In(c.Location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, c.Location)
	tracking, err := queries.ReportTrackingStarted(ctx)
	if err != nil {
		return reporting.Report{}, err
	}
	partial := tracking.Time.After(start)
	if partial {
		start = tracking.Time
	}
	rows, err := queries.ReportAccounts(ctx, storedb.ReportAccountsParams{WindowStart: pgtype.Timestamptz{Time: start, Valid: true}, WindowEnd: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return reporting.Report{}, err
	}
	accounts := make([]reporting.Account, 0, len(rows))
	for _, row := range rows {
		a := reporting.Account{ID: row.ID, Name: row.DisplayName, Email: row.Email, Requests: row.Requests, Snapshot: row.QuotaSnapshot, CheckedAt: row.QuotaCheckedAt.Time}
		if err := json.Unmarshal(row.Employees, &a.Employees); err != nil {
			return reporting.Report{}, err
		}
		accounts = append(accounts, a)
	}
	unattributed, err := queries.ReportUnattributedRequests(ctx, storedb.ReportUnattributedRequestsParams{WindowStart: pgtype.Timestamptz{Time: start, Valid: true}, WindowEnd: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return reporting.Report{}, err
	}
	return reporting.NewReport(c, accounts, start, now, now, partial, unattributed), nil
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
