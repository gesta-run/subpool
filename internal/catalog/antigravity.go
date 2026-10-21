package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/provider/antigravity"
)

type AntigravityModels interface {
	Models(context.Context, antigravity.Credentials) (antigravity.Snapshot, error)
}

func (s *Service) WithAntigravity(client AntigravityModels) *Service {
	s.antigravity = client
	return s
}

func (s *Service) listAntigravity(ctx context.Context, account domain.ProviderAccount) ([]domain.ProviderModel, error) {
	if s.antigravity == nil {
		return nil, errors.New("Antigravity model discovery is unavailable")
	}
	credentials, err := decrypt[antigravity.Credentials](s.cipher, account)
	if err != nil {
		return nil, err
	}
	refreshed := false
	if time.Until(credentials.ExpiresAt) <= time.Minute {
		if s.refresher == nil {
			return nil, errors.New("Antigravity credential refresh is unavailable")
		}
		account, err = s.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
		if err != nil {
			return nil, err
		}
		credentials, err = decrypt[antigravity.Credentials](s.cipher, account)
		if err != nil {
			return nil, err
		}
		refreshed = true
	}
	snapshot, err := s.antigravity.Models(ctx, credentials)
	if antigravity.IsDefinitiveAuthError(err) && !refreshed && s.refresher != nil {
		account, err = s.refresher.RefreshAccount(ctx, account.ID, account.CredentialVersion)
		if err == nil {
			credentials, err = decrypt[antigravity.Credentials](s.cipher, account)
		}
		if err == nil {
			snapshot, err = s.antigravity.Models(ctx, credentials)
		}
	}
	if err != nil {
		return nil, err
	}
	models := make([]domain.ProviderModel, 0, len(snapshot.Models))
	for name, model := range snapshot.Models {
		models = append(models, domain.ProviderModel{ID: name, DisplayName: model.DisplayName})
	}
	return models, nil
}
