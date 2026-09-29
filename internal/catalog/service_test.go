package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/copilot"
)

type passthroughCipher struct{}

func (passthroughCipher) Decrypt(value []byte) ([]byte, error) {
	return value, nil
}

type copilotModelsStub struct {
	payload string
}

func (s copilotModelsStub) Models(context.Context, copilot.Credentials) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(s.payload)),
	}, nil
}

func TestListCopilotFiltersUnsupportedEndpointModels(t *testing.T) {
	credentials, _ := json.Marshal(copilot.Credentials{GitHubToken: "github-token"})
	account := domain.ProviderAccount{Provider: domain.ProviderCopilot, CredentialCiphertext: credentials}
	provider := copilotModelsStub{payload: `{"data":[
		{"id":"chat-model","supported_endpoints":["/v1/chat/completions"]},
		{"id":"responses-model","supported_endpoints":["/responses"]},
		{"name":"name-only-model","supported_endpoints":["/responses"]},
		{"id":"legacy-model"},
		{"display_name":"Missing identifier","supported_endpoints":["/responses"]},
		{"id":"messages-model","supported_endpoints":["/v1/messages"]},
		{"id":"websocket-model","supported_endpoints":["ws:/responses"]}
	]}`}
	service := New(passthroughCipher{}, nil, nil, nil, provider)

	models, err := service.ListAccount(context.Background(), account)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 || models[0].ID != "chat-model" || models[1].ID != "legacy-model" ||
		models[2].ID != "name-only-model" || models[3].ID != "responses-model" {
		t.Fatalf("models = %#v", models)
	}
}
