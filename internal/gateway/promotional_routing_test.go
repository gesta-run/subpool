package gateway

import (
	"bytes"
	"encoding/json"
	"github.com/gesta-run/subpool/internal/adapter"
	"github.com/gesta-run/subpool/internal/credential"
	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/openaibilling"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPromotionalGuardRejectsExpiredAndMissingSnapshotBeforeUpstream(t *testing.T) {
	s, st, _, _ := newTestServer(t)
	s.WithPromotionalAPIFallback(true)
	cipher, _ := credential.New(bytes.Repeat([]byte{3}, 32))
	raw, _ := json.Marshal(openaicompat.Credentials{BaseURL: "https://api.openai.com/v1", APIKey: "synthetic-key"})
	encrypted, _ := cipher.Encrypt(raw)
	now := time.Now()
	st.route.Pool.Provider = domain.ProviderMixed
	account := domain.ProviderAccount{Provider: domain.ProviderOpenAICompatible, CredentialType: domain.CredentialAPIKey, CredentialCiphertext: encrypted, QuotaCheckedAt: &now}
	expired, _ := json.Marshal(openaibilling.Snapshot{PlanType: openaibilling.PlanType, Balance: openaibilling.Balance{Granted: 20, Remaining: 10, ExpiresAt: now.Add(-time.Second).Unix()}})
	for _, snapshot := range [][]byte{nil, expired} {
		account.QuotaSnapshot = snapshot
		response, retry, complete := s.attemptAccount(httptest.NewRequest("POST", "/v1/responses", nil), st.route, adapter.Request{}, account)
		if response != nil || retry != retryUnavailable || complete {
			t.Fatal("invalid grant was not rejected before calling the nil API client")
		}
	}
}

func TestDisabledAPIMembershipReassignsOnlyOnce(t *testing.T) {
	s, st, _, plain := newTestServer(t)
	cipher := s.cipher.(*credential.Cipher)
	old := compatibleAccountWithCipher(t, cipher, "disabled-api")
	replacement := compatibleAccountWithCipher(t, cipher, "remaining-api")
	st.route.Account = old
	st.route.Pool.Provider = domain.ProviderMixed
	st.route.MembershipEnabled = false
	st.reassignedAccounts = []domain.ProviderAccount{replacement}
	s.compatible = &fakeCompatibleProvider{}
	response := serveGateway(t, s, plain, "/v1/responses", `{"model":"gpt-test","input":"hello"}`)
	if response.Code != 200 {
		t.Fatalf("remaining account was skipped: %d %s", response.Code, response.Body.String())
	}
	if len(st.reassignExcludes) != 1 || len(st.reassignExcludes[0]) != 1 || st.reassignExcludes[0][0] != old.ID {
		t.Fatalf("reassignments=%v", st.reassignExcludes)
	}
}

func TestWebSocketDisabledAPIMembershipReassignsOnlyOnce(t *testing.T) {
	s, st, _, plain := newTestServer(t)
	cipher := s.cipher.(*credential.Cipher)
	old := compatibleAccountWithCipher(t, cipher, "disabled-api")
	replacement := compatibleAccountWithCipher(t, cipher, "remaining-api")
	st.route.Account = old
	st.route.Pool.Provider = domain.ProviderMixed
	st.route.MembershipEnabled = false
	st.reassignedAccounts = []domain.ProviderAccount{replacement}
	provider := &responsesWSBridgeProvider{payload: make(chan []byte, 1)}
	s.compatible = provider
	s.WithResponsesWebSocket(true, false, "")
	client, cleanup := dialResponsesWSTestServer(t, s, plain)
	defer cleanup()
	writeResponsesWSMessage(t, client, `{"type":"response.create","model":"gpt-test","input":"hello"}`)
	event := readResponsesWSMessage(t, client)
	if event["type"] != "response.completed" {
		t.Fatalf("remaining account was skipped: %#v", event)
	}
	<-provider.payload
	if len(st.reassignExcludes) != 1 || len(st.reassignExcludes[0]) != 1 || st.reassignExcludes[0][0] != old.ID {
		t.Fatalf("reassignments=%v", st.reassignExcludes)
	}
}
