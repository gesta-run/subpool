package codex

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppServerReadsAndConsumesResetCredits(t *testing.T) {
	executable := codexTestExecutable(t)
	client := &AppServer{executable: executable}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	credentials := Credentials{AccessToken: "test-access-token", AccountID: "test-account"}
	credits, err := client.ReadResetCredits(ctx, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if credits == nil || credits.AvailableCount != 2 || len(credits.Credits) != 1 || credits.Credits[0].ID != "credit-1" {
		t.Fatalf("credits = %#v", credits)
	}
	result, err := client.ConsumeResetCredit(ctx, credentials, "credit-1", "00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "reset" || result.ResetCredits == nil || result.ResetCredits.AvailableCount != 1 {
		t.Fatalf("result = %#v", result)
	}
	models, err := client.ListModels(ctx, credentials)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Model != "model-alpha" || models[0].DisplayName != "Model Alpha" || len(models[0].SupportedReasoningEfforts) != 1 {
		t.Fatalf("models = %#v", models)
	}
}

func TestDeviceAuthReturnsOneTimeCodeAndCredentials(t *testing.T) {
	client := NewDeviceAuth()
	client.executable = codexTestExecutable(t)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authorization, result, err := client.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if authorization.LoginID == "" || authorization.UserCode != "ABCD-EFGH" || authorization.VerificationURL != "https://auth.openai.com/device" {
		t.Fatalf("authorization = %#v", authorization)
	}
	completed := <-result
	if completed.Err != nil {
		t.Fatal(completed.Err)
	}
	if completed.Credentials.AccessToken == "" || completed.Credentials.RefreshToken != "device-refresh" || completed.Credentials.AccountID != "device-account" || completed.Credentials.Email != "device@example.com" {
		t.Fatalf("credentials = %#v", completed.Credentials)
	}
	if completed.Credentials.ExpiresAt.IsZero() {
		t.Fatal("access token expiry was not parsed")
	}
}

func TestDeviceAuthStartHonorsRequestCancellation(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex-hanging-test")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nwhile IFS= read -r line; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	client := NewDeviceAuth()
	client.executable = executable
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := client.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("device authorization did not stop promptly")
	}
}

func codexTestExecutable(t *testing.T) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "codex-test")
	wrapper := fmt.Sprintf("#!/bin/sh\nSUBPOOL_CODEX_HELPER=1 exec %q -test.run=TestCodexAppServerHelperProcess -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(executable, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestCodexAppServerHelperProcess(t *testing.T) {
	if os.Getenv("SUBPOOL_CODEX_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	consumed := false
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		var result any = map[string]any{}
		deviceLogin := false
		switch request.Method {
		case "account/login/start":
			var params struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Type == "chatgptDeviceCode" {
				deviceLogin = true
				result = map[string]any{"type": "chatgptDeviceCode", "loginId": "upstream-login", "userCode": "ABCD-EFGH", "verificationUrl": "https://auth.openai.com/device"}
				payload, _ := json.Marshal(map[string]any{"email": "device@example.com", "exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "device-account"}})
				token := fmt.Sprintf("header.%s.signature", base64.RawURLEncoding.EncodeToString(payload))
				authFile, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"access_token": token, "refresh_token": "device-refresh", "id_token": token, "account_id": "device-account"}})
				_ = os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), authFile, 0o600)
			} else {
				result = map[string]any{"type": "chatgptAuthTokens"}
			}
		case "account/rateLimitResetCredit/consume":
			consumed = true
			result = map[string]any{"outcome": "reset"}
		case "account/rateLimits/read":
			available := 2
			if consumed {
				available = 1
			}
			result = map[string]any{
				"rateLimits": map[string]any{"limitId": "codex", "primary": map[string]any{"usedPercent": 25}},
				"rateLimitResetCredits": map[string]any{
					"availableCount": available,
					"credits": []map[string]any{{
						"id": "credit-1", "resetType": "codexRateLimits", "status": "available",
						"grantedAt": 1781654400, "expiresAt": 1784246400,
					}},
				},
			}
		case "model/list":
			result = map[string]any{
				"data": []map[string]any{{
					"id": "catalog-alpha", "model": "model-alpha", "displayName": "Model Alpha", "description": "General-purpose model",
					"hidden": false, "isDefault": true, "inputModalities": []string{"text", "image"},
					"supportedReasoningEfforts": []map[string]any{{"reasoningEffort": "high", "description": "More reasoning"}},
				}},
				"nextCursor": nil,
			}
		}
		response, _ := json.Marshal(map[string]any{"id": request.ID, "result": result})
		_, _ = fmt.Fprintln(os.Stdout, string(response))
		if deviceLogin {
			notification, _ := json.Marshal(map[string]any{"method": "account/login/completed", "params": map[string]any{"loginId": "upstream-login", "success": true}})
			_, _ = fmt.Fprintln(os.Stdout, string(notification))
		}
	}
	os.Exit(0)
}
