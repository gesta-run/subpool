package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppServerReadsAndConsumesResetCredits(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex-test")
	wrapper := fmt.Sprintf("#!/bin/sh\nSUBPOOL_CODEX_HELPER=1 exec %q -test.run=TestCodexAppServerHelperProcess -- \"$@\"\n", os.Args[0])
	if err := os.WriteFile(executable, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	client := NewAppServer(executable)
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
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		var result any = map[string]any{}
		switch request.Method {
		case "account/login/start":
			result = map[string]any{"type": "chatgptAuthTokens"}
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
	}
	os.Exit(0)
}
