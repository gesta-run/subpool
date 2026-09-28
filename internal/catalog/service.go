package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/codex"
	"github.com/gesta-run/subpool/internal/provider/copilot"
	"github.com/gesta-run/subpool/internal/provider/openaicompat"
)

type Cipher interface {
	Decrypt([]byte) ([]byte, error)
}

type Refresher interface {
	RefreshAccount(context.Context, string, int) (domain.ProviderAccount, error)
}

type CodexModels interface {
	ListModels(context.Context, codex.Credentials) ([]codex.Model, error)
}

type CompatibleModels interface {
	Models(context.Context, openaicompat.Credentials) (*http.Response, error)
}

type CopilotModels interface {
	Models(context.Context, copilot.Credentials) (*http.Response, error)
}

type Service struct {
	cipher     Cipher
	refresher  Refresher
	codex      CodexModels
	compatible CompatibleModels
	copilot    CopilotModels
}

func New(cipher Cipher, refresher Refresher, codexModels CodexModels, compatibleModels CompatibleModels, copilotModels ...CopilotModels) *Service {
	service := &Service{cipher: cipher, refresher: refresher, codex: codexModels, compatible: compatibleModels}
	if len(copilotModels) > 0 {
		service.copilot = copilotModels[0]
	}
	return service
}

func (s *Service) ListAccount(ctx context.Context, account domain.ProviderAccount) ([]domain.ProviderModel, error) {
	var (
		models []domain.ProviderModel
		err    error
	)
	switch account.Provider {
	case domain.ProviderCodex:
		models, err = s.listCodex(ctx, account)
	case domain.ProviderOpenAICompatible:
		models, err = s.listCompatible(ctx, account)
	case domain.ProviderCopilot:
		models, err = s.listCopilot(ctx, account)
	default:
		err = errors.New("provider does not support model discovery")
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].IsDefault != models[j].IsDefault {
			return models[i].IsDefault
		}
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models, nil
}

func (s *Service) listCopilot(ctx context.Context, account domain.ProviderAccount) ([]domain.ProviderModel, error) {
	if s.copilot == nil {
		return nil, errors.New("Copilot model discovery is unavailable")
	}
	credentials, err := decrypt[copilot.Credentials](s.cipher, account)
	if err != nil {
		return nil, err
	}
	response, err := s.copilot.Models(ctx, credentials)
	if err != nil {
		return nil, err
	}
	upstream, err := copilot.DecodeModels(response)
	if err != nil {
		return nil, err
	}
	models := make([]domain.ProviderModel, 0, len(upstream))
	seen := make(map[string]struct{}, len(upstream))
	for _, model := range upstream {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			modelID = strings.TrimSpace(model.Name)
		}
		if modelID == "" || model.Disabled || model.Hidden {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, domain.ProviderModel{ID: modelID, DisplayName: model.DisplayName})
	}
	return models, nil
}

func (s *Service) listCodex(ctx context.Context, account domain.ProviderAccount) ([]domain.ProviderModel, error) {
	if s.codex == nil {
		return nil, errors.New("Codex model discovery is unavailable")
	}
	credentials, err := decrypt[codex.Credentials](s.cipher, account)
	if err != nil {
		return nil, err
	}
	if !credentials.ExpiresAt.IsZero() && time.Until(credentials.ExpiresAt) <= 30*time.Second {
		account, err = s.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
		if err != nil {
			return nil, err
		}
		credentials, err = decrypt[codex.Credentials](s.cipher, account)
		if err != nil {
			return nil, err
		}
	}
	upstreamModels, err := s.codex.ListModels(ctx, credentials)
	if err != nil {
		return nil, err
	}
	models := make([]domain.ProviderModel, 0, len(upstreamModels))
	seen := make(map[string]struct{}, len(upstreamModels))
	for _, upstream := range upstreamModels {
		modelID := strings.TrimSpace(upstream.Model)
		if modelID == "" {
			modelID = strings.TrimSpace(upstream.ID)
		}
		if modelID == "" || upstream.Hidden {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		reasoningEfforts := make([]string, 0, len(upstream.SupportedReasoningEfforts))
		for _, option := range upstream.SupportedReasoningEfforts {
			if effort := strings.TrimSpace(option.ReasoningEffort); effort != "" {
				reasoningEfforts = append(reasoningEfforts, effort)
			}
		}
		models = append(models, domain.ProviderModel{
			ID: modelID, DisplayName: upstream.DisplayName, Description: upstream.Description,
			IsDefault: upstream.IsDefault, ReasoningEfforts: reasoningEfforts, InputModalities: upstream.InputModalities,
		})
	}
	return models, nil
}

func (s *Service) listCompatible(ctx context.Context, account domain.ProviderAccount) ([]domain.ProviderModel, error) {
	if s.compatible == nil {
		return nil, errors.New("OpenAI-compatible model discovery is unavailable")
	}
	credentials, err := decrypt[openaicompat.Credentials](s.cipher, account)
	if err != nil {
		return nil, err
	}
	response, err := s.compatible.Models(ctx, credentials)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return nil, errors.New("upstream model endpoint rejected the request")
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	models := make([]domain.ProviderModel, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, upstream := range payload.Data {
		modelID := strings.TrimSpace(upstream.ID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, domain.ProviderModel{ID: modelID})
	}
	return models, nil
}

func decrypt[T any](cipher Cipher, account domain.ProviderAccount) (T, error) {
	var credentials T
	plaintext, err := cipher.Decrypt(account.CredentialCiphertext)
	if err != nil {
		return credentials, err
	}
	if err = json.Unmarshal(plaintext, &credentials); err != nil {
		return credentials, err
	}
	return credentials, nil
}
