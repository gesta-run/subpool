package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	Outcome      string               `json:"outcome"`
	ResetCredits *ResetCreditsSummary `json:"reset_credits"`
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

func NewAppServer(executable string) *AppServer {
	if strings.TrimSpace(executable) == "" {
		executable = defaultCodexExecutable
	}
	return &AppServer{executable: executable}
}

func (a *AppServer) ReadResetCredits(ctx context.Context, credentials Credentials) (*ResetCreditsSummary, error) {
	session, err := startAppServerSession(ctx, a.executable, credentials)
	if err != nil {
		return nil, err
	}
	defer session.close()
	return session.readResetCredits(3)
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
	credits, err := session.readResetCredits(4)
	if err != nil {
		return ConsumeResetCreditResult{}, err
	}
	return ConsumeResetCreditResult{Outcome: consumed.Outcome, ResetCredits: credits}, nil
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
	credentials Credentials
}

func startAppServerSession(ctx context.Context, executable string, credentials Credentials) (*appServerSession, error) {
	if strings.TrimSpace(credentials.AccessToken) == "" || strings.TrimSpace(credentials.AccountID) == "" {
		return nil, fmt.Errorf("Codex credentials are incomplete")
	}
	configDir, err := os.MkdirTemp("", "subpool-codex-")
	if err != nil {
		return nil, fmt.Errorf("create temporary Codex config: %w", err)
	}
	cmd := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://")
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
	session := &appServerSession{cmd: cmd, stdin: stdin, scanner: scanner, stderr: stderr, credentials: credentials}
	if err = session.call(1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "subpool", "title": "Subpool", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}, nil); err != nil {
		session.close()
		_ = os.RemoveAll(configDir)
		return nil, err
	}
	if err = session.notify("initialized", map[string]any{}); err != nil {
		session.close()
		_ = os.RemoveAll(configDir)
		return nil, err
	}
	if err = session.call(2, "account/login/start", map[string]any{
		"type":             "chatgptAuthTokens",
		"accessToken":      credentials.AccessToken,
		"chatgptAccountId": credentials.AccountID,
	}, nil); err != nil {
		session.close()
		_ = os.RemoveAll(configDir)
		return nil, err
	}
	return session, nil
}

func (s *appServerSession) readResetCredits(id int) (*ResetCreditsSummary, error) {
	var response struct {
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
		return nil, err
	}
	if response.RateLimitResetCredits == nil {
		return nil, nil
	}
	result := &ResetCreditsSummary{AvailableCount: response.RateLimitResetCredits.AvailableCount}
	if response.RateLimitResetCredits.Credits != nil {
		result.Credits = make([]ResetCredit, 0, len(response.RateLimitResetCredits.Credits))
		for _, credit := range response.RateLimitResetCredits.Credits {
			result.Credits = append(result.Credits, ResetCredit{
				ID: credit.ID, ResetType: credit.ResetType, Status: credit.Status, GrantedAt: credit.GrantedAt,
				ExpiresAt: credit.ExpiresAt, Title: credit.Title, Description: credit.Description,
			})
		}
	}
	return result, nil
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
	_ = s.stdin.Close()
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.cmd.Wait()
	if value := s.cmd.Env; value != nil {
		for _, entry := range value {
			if strings.HasPrefix(entry, "CODEX_HOME=") {
				_ = os.RemoveAll(strings.TrimPrefix(entry, "CODEX_HOME="))
				break
			}
		}
	}
}
