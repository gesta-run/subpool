package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const defaultCodexExecutable = "codex"

type ResetCredit struct {
	ID          string `json:"id"`
	ResetType   string `json:"reset_type"`
	Status      string `json:"status"`
	GrantedAt   int64  `json:"granted_at"`
	ExpiresAt   *int64 `json:"expires_at"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

type ResetCreditsSummary struct {
	AvailableCount int           `json:"available_count"`
	Credits        []ResetCredit `json:"credits"`
}

type ConsumeResetCreditResult struct {
	Outcome       string               `json:"outcome"`
	ResetCredits  *ResetCreditsSummary `json:"reset_credits"`
	QuotaSnapshot *UsageSnapshot       `json:"quota_snapshot,omitempty"`
}

type Model struct {
	ID                        string                  `json:"id"`
	Model                     string                  `json:"model"`
	DisplayName               string                  `json:"display_name"`
	Description               string                  `json:"description"`
	Hidden                    bool                    `json:"hidden"`
	IsDefault                 bool                    `json:"is_default"`
	InputModalities           []string                `json:"input_modalities"`
	SupportedReasoningEfforts []ReasoningEffortOption `json:"supported_reasoning_efforts"`
}

type ReasoningEffortOption struct {
	ReasoningEffort string `json:"reasoning_effort"`
	Description     string `json:"description"`
}

type AppServer struct {
	executable string
}

func NewAppServer() *AppServer {
	return &AppServer{executable: defaultCodexExecutable}
}

func (a *AppServer) readRateLimits(ctx context.Context, credentials Credentials) (appServerRateLimitSnapshot, error) {
	session, err := startAppServerSession(ctx, a.executable, credentials)
	if err != nil {
		return appServerRateLimitSnapshot{}, err
	}
	defer session.close()
	return session.readRateLimits(3)
}

func (a *AppServer) Usage(ctx context.Context, credentials Credentials) (UsageSnapshot, error) {
	snapshot, err := a.readRateLimits(ctx, credentials)
	if err != nil {
		return UsageSnapshot{}, err
	}
	if snapshot.Usage == nil {
		return UsageSnapshot{}, fmt.Errorf("Codex rate limits response is missing usage")
	}
	return *snapshot.Usage, nil
}

func (a *AppServer) ReadResetCredits(ctx context.Context, credentials Credentials) (*ResetCreditsSummary, error) {
	snapshot, err := a.readRateLimits(ctx, credentials)
	return snapshot.ResetCredits, err
}

func (a *AppServer) ConsumeResetCredit(ctx context.Context, credentials Credentials, creditID, idempotencyKey string) (ConsumeResetCreditResult, error) {
	session, err := startAppServerSession(ctx, a.executable, credentials)
	if err != nil {
		return ConsumeResetCreditResult{}, err
	}
	defer session.close()
	params := map[string]any{"idempotencyKey": idempotencyKey}
	if strings.TrimSpace(creditID) != "" {
		params["creditId"] = creditID
	}
	var consumed struct {
		Outcome string `json:"outcome"`
	}
	if err = session.call(3, "account/rateLimitResetCredit/consume", params, &consumed); err != nil {
		return ConsumeResetCreditResult{}, err
	}
	snapshot, err := session.readRateLimits(4)
	if err != nil {
		slog.Warn("Codex reset completed but rate limit refresh failed", "outcome", consumed.Outcome, "error", err)
		return ConsumeResetCreditResult{Outcome: consumed.Outcome}, nil
	}
	return ConsumeResetCreditResult{Outcome: consumed.Outcome, ResetCredits: snapshot.ResetCredits, QuotaSnapshot: snapshot.Usage}, nil
}

func (a *AppServer) ListModels(ctx context.Context, credentials Credentials) ([]Model, error) {
	session, err := startAppServerSession(ctx, a.executable, credentials)
	if err != nil {
		return nil, err
	}
	defer session.close()

	models := make([]Model, 0)
	cursor := ""
	for requestID := 3; requestID < 23; requestID++ {
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var response struct {
			Data []struct {
				ID                        string                  `json:"id"`
				Model                     string                  `json:"model"`
				DisplayName               string                  `json:"displayName"`
				Description               string                  `json:"description"`
				Hidden                    bool                    `json:"hidden"`
				IsDefault                 bool                    `json:"isDefault"`
				InputModalities           []string                `json:"inputModalities"`
				SupportedReasoningEfforts []ReasoningEffortOption `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err = session.call(requestID, "model/list", params, &response); err != nil {
			return nil, err
		}
		for _, model := range response.Data {
			models = append(models, Model{
				ID: model.ID, Model: model.Model, DisplayName: model.DisplayName, Description: model.Description,
				Hidden: model.Hidden, IsDefault: model.IsDefault, InputModalities: model.InputModalities,
				SupportedReasoningEfforts: model.SupportedReasoningEfforts,
			})
		}
		if response.NextCursor == nil || strings.TrimSpace(*response.NextCursor) == "" || *response.NextCursor == cursor {
			return models, nil
		}
		cursor = *response.NextCursor
	}
	return nil, fmt.Errorf("Codex model list exceeded the pagination limit")
}

type appServerSession struct {
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	scanner     *bufio.Scanner
	stderr      *bytes.Buffer
	configDir   string
	credentials Credentials
	closeOnce   sync.Once
}

func startAppServerSession(ctx context.Context, executable string, credentials Credentials) (*appServerSession, error) {
	if strings.TrimSpace(credentials.AccessToken) == "" || strings.TrimSpace(credentials.AccountID) == "" {
		return nil, fmt.Errorf("Codex credentials are incomplete")
	}
	session, err := startAppServerProcess(ctx, executable, credentials)
	if err != nil {
		return nil, err
	}
	if err = session.call(2, "account/login/start", map[string]any{
		"type":             "chatgptAuthTokens",
		"accessToken":      credentials.AccessToken,
		"chatgptAccountId": credentials.AccountID,
	}, nil); err != nil {
		session.close()
		return nil, err
	}
	return session, nil
}

func startAppServerProcess(ctx context.Context, executable string, credentials Credentials) (*appServerSession, error) {
	session, err := launchAppServerProcess(ctx, executable, credentials)
	if err != nil {
		return nil, err
	}
	if err = initializeAppServer(ctx, session); err != nil {
		session.close()
		return nil, err
	}
	return session, nil
}

func launchAppServerProcess(ctx context.Context, executable string, credentials Credentials) (*appServerSession, error) {
	configDir, err := os.MkdirTemp("", "subpool-codex-")
	if err != nil {
		return nil, fmt.Errorf("create temporary Codex config: %w", err)
	}
	cmd := exec.CommandContext(ctx, executable, "-c", `cli_auth_credentials_store="file"`, "app-server", "--listen", "stdio://")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+configDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(configDir)
		return nil, fmt.Errorf("open Codex app-server input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = os.RemoveAll(configDir)
		return nil, fmt.Errorf("open Codex app-server output: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		_ = os.RemoveAll(configDir)
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	return &appServerSession{cmd: cmd, stdin: stdin, scanner: scanner, stderr: stderr, configDir: configDir, credentials: credentials}, nil
}

func initializeAppServer(ctx context.Context, session *appServerSession) error {
	if err := session.callContext(ctx, 1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "subpool", "title": "Subpool", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		return err
	}
	if err := session.notify("initialized", map[string]any{}); err != nil {
		return err
	}
	return nil
}

type appServerRateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins *int64  `json:"windowDurationMins"`
	ResetsAt           *int64  `json:"resetsAt"`
}

type appServerRateLimits struct {
	PlanType             *string                   `json:"planType"`
	Primary              *appServerRateLimitWindow `json:"primary"`
	Secondary            *appServerRateLimitWindow `json:"secondary"`
	RateLimitReachedType *string                   `json:"rateLimitReachedType"`
	SpendControlReached  *bool                     `json:"spendControlReached"`
}

type appServerRateLimitSnapshot struct {
	ResetCredits *ResetCreditsSummary
	Usage        *UsageSnapshot
}

func (s *appServerSession) readRateLimits(id int) (appServerRateLimitSnapshot, error) {
	var response struct {
		RateLimits            appServerRateLimits `json:"rateLimits"`
		RateLimitResetCredits *struct {
			AvailableCount int `json:"availableCount"`
			Credits        []struct {
				ID          string `json:"id"`
				ResetType   string `json:"resetType"`
				Status      string `json:"status"`
				GrantedAt   int64  `json:"grantedAt"`
				ExpiresAt   *int64 `json:"expiresAt"`
				Title       string `json:"title"`
				Description string `json:"description"`
			} `json:"credits"`
		} `json:"rateLimitResetCredits"`
	}
	if err := s.call(id, "account/rateLimits/read", nil, &response); err != nil {
		return appServerRateLimitSnapshot{}, err
	}
	result := appServerRateLimitSnapshot{Usage: normalizeAppServerRateLimits(response.RateLimits)}
	if response.RateLimitResetCredits == nil {
		return result, nil
	}
	result.ResetCredits = &ResetCreditsSummary{AvailableCount: response.RateLimitResetCredits.AvailableCount}
	if response.RateLimitResetCredits.Credits != nil {
		result.ResetCredits.Credits = make([]ResetCredit, 0, len(response.RateLimitResetCredits.Credits))
		for _, credit := range response.RateLimitResetCredits.Credits {
			result.ResetCredits.Credits = append(result.ResetCredits.Credits, ResetCredit{
				ID: credit.ID, ResetType: credit.ResetType, Status: credit.Status, GrantedAt: credit.GrantedAt,
				ExpiresAt: credit.ExpiresAt, Title: credit.Title, Description: credit.Description,
			})
		}
	}
	return result, nil
}

func normalizeAppServerRateLimits(limits appServerRateLimits) *UsageSnapshot {
	if limits.PlanType == nil && limits.Primary == nil && limits.Secondary == nil && limits.RateLimitReachedType == nil && limits.SpendControlReached == nil {
		return nil
	}
	allowed := true
	snapshot := &UsageSnapshot{UsageAllowed: &allowed}
	if limits.PlanType != nil {
		snapshot.PlanType = *limits.PlanType
	}
	primary := normalizeAppServerWindow(limits.Primary)
	secondary := normalizeAppServerWindow(limits.Secondary)
	snapshot.FiveHour = primary
	snapshot.Weekly = secondary
	const weeklyWindowSeconds = 6 * 24 * 60 * 60
	if primary != nil && primary.WindowSeconds >= weeklyWindowSeconds {
		snapshot.Weekly = primary
		snapshot.FiveHour = secondary
	} else if secondary != nil && secondary.WindowSeconds < weeklyWindowSeconds {
		snapshot.Weekly = nil
	}
	if limits.RateLimitReachedType != nil && *limits.RateLimitReachedType != "" && *limits.RateLimitReachedType != "unknown" {
		snapshot.markUsageBlocked(*limits.RateLimitReachedType)
	} else if limits.SpendControlReached != nil && *limits.SpendControlReached {
		snapshot.markUsageBlocked("spend_control_reached")
	} else if (snapshot.FiveHour != nil && snapshot.FiveHour.UsedPercent >= 100) || (snapshot.Weekly != nil && snapshot.Weekly.UsedPercent >= 100) {
		snapshot.markUsageBlocked("rate_limit_reached")
	}
	return snapshot
}

func normalizeAppServerWindow(window *appServerRateLimitWindow) *UsageWindow {
	if window == nil {
		return nil
	}
	var durationSeconds, resetAt int64
	if window.WindowDurationMins != nil {
		durationSeconds = *window.WindowDurationMins * 60
	}
	if window.ResetsAt != nil {
		resetAt = *window.ResetsAt
	}
	return normalizeUsageWindow(window.UsedPercent, durationSeconds, resetAt)
}

func (s *appServerSession) call(id int, method string, params any, target any) error {
	request := map[string]any{"method": method, "id": id}
	if params != nil {
		request["params"] = params
	}
	if err := s.write(request); err != nil {
		return err
	}
	wantedID := strconv.Itoa(id)
	for s.scanner.Scan() {
		var message struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(s.scanner.Bytes(), &message); err != nil {
			return fmt.Errorf("decode Codex app-server response: %w", err)
		}
		if message.Method == "account/chatgptAuthTokens/refresh" && len(message.ID) > 0 {
			if err := s.write(map[string]any{
				"id":     message.ID,
				"result": map[string]any{"accessToken": s.credentials.AccessToken, "chatgptAccountId": s.credentials.AccountID},
			}); err != nil {
				return err
			}
			continue
		}
		if string(message.ID) != wantedID {
			continue
		}
		if message.Error != nil {
			return fmt.Errorf("Codex app-server %s failed (%d): %s", method, message.Error.Code, message.Error.Message)
		}
		if target != nil && len(message.Result) > 0 {
			if err := json.Unmarshal(message.Result, target); err != nil {
				return fmt.Errorf("decode Codex app-server %s result: %w", method, err)
			}
		}
		return nil
	}
	if err := s.scanner.Err(); err != nil {
		return fmt.Errorf("read Codex app-server response: %w", err)
	}
	detail := strings.TrimSpace(s.stderr.String())
	if len(detail) > 300 {
		detail = detail[:300]
	}
	if detail != "" {
		return fmt.Errorf("Codex app-server stopped before %s completed: %s", method, detail)
	}
	return fmt.Errorf("Codex app-server stopped before %s completed", method)
}

func (s *appServerSession) callContext(ctx context.Context, id int, method string, params any, target any) error {
	done := make(chan error, 1)
	go func() {
		done <- s.call(id, method, params, target)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		s.close()
		return ctx.Err()
	}
}

func (s *appServerSession) notify(method string, params any) error {
	return s.write(map[string]any{"method": method, "params": params})
}

func (s *appServerSession) write(message any) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode Codex app-server request: %w", err)
	}
	payload = append(payload, '\n')
	if _, err = s.stdin.Write(payload); err != nil {
		return fmt.Errorf("write Codex app-server request: %w", err)
	}
	return nil
}

func (s *appServerSession) close() {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		_ = s.cmd.Wait()
		_ = os.RemoveAll(s.configDir)
	})
}
