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
