package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

type fakeCopilotDeviceAuth struct {
	mu       sync.Mutex
	result   chan copilot.DeviceAuthorizationResult
	canceled string
}

func (f *fakeCopilotDeviceAuth) Start(context.Context) (copilot.DeviceAuthorization, <-chan copilot.DeviceAuthorizationResult, error) {
	f.result = make(chan copilot.DeviceAuthorizationResult)
	return copilot.DeviceAuthorization{
		LoginID: "copilot-login", UserCode: "ABCD-EFGH",
		VerificationURL: "https://github.com/login/device", ExpiresAt: time.Now().Add(time.Minute),
	}, f.result, nil
}

func (f *fakeCopilotDeviceAuth) Cancel(loginID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = loginID
	close(f.result)
}

func TestCopilotDeviceLoginCanStartAndCancel(t *testing.T) {
	auth := &fakeCopilotDeviceAuth{}
	server := &Server{copilotDeviceAuth: auth, logins: make(map[string]*deviceLoginAttempt)}
	start := httptest.NewRecorder()
	server.startCopilotDeviceLogin(start, httptest.NewRequest(http.MethodPost, "/api/v1/provider-accounts/copilot/device-login", strings.NewReader(`{"display_name":"Primary Copilot"}`)))
	if start.Code != http.StatusCreated || !strings.Contains(start.Body.String(), `"user_code":"ABCD-EFGH"`) {
		t.Fatalf("status = %d, body=%s", start.Code, start.Body.String())
	}
	cancel := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/provider-accounts/copilot/device-login/copilot-login", nil)
	request.SetPathValue("id", "copilot-login")
	server.cancelDeviceLogin(cancel, request)
	if cancel.Code != http.StatusNoContent || auth.canceled != "copilot-login" {
		t.Fatalf("status/canceled = %d/%q", cancel.Code, auth.canceled)
	}
}

func TestSaveCopilotAccountUsesStableGitHubUserID(t *testing.T) {
	server, st, cipher := newControlServer(t)
	credentials := copilot.Credentials{GitHubToken: "github-token", Login: "renameable-login", UserID: 42, Email: "octocat@example.com"}
	accountID, err := server.saveCopilotAccount(context.Background(), credentials, "Primary Copilot")
	if err != nil {
		t.Fatal(err)
	}
	if accountID == "" || st.account.Provider != domain.ProviderCopilot || st.account.CredentialType != domain.CredentialSubscription {
		t.Fatalf("account = %#v", st.account)
	}
	wantSubject := server.keys.Digest("provider-subject:" + domain.ProviderCopilot + ":42")
	if !bytes.Equal(st.account.SubjectHMAC, wantSubject) {
		t.Fatal("Copilot subject is not based on the stable GitHub user ID")
	}
	plaintext, err := cipher.Decrypt(st.account.CredentialCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	var stored copilot.Credentials
	if err = json.Unmarshal(plaintext, &stored); err != nil || stored.GitHubToken != credentials.GitHubToken || stored.UserID != credentials.UserID {
		t.Fatalf("stored credentials = %#v, error = %v", stored, err)
	}
}
