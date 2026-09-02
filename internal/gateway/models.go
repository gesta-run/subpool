package gateway

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
)

const (
	modelCacheTTL        = 5 * time.Minute
	modelDiscoveryWorker = 4
)

type exposedModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type modelCacheEntry struct {
	models    []exposedModel
	expiresAt time.Time
}

type modelCache struct {
	mu        sync.Mutex
	entries   map[string]modelCacheEntry
	lastSweep time.Time
}

func newModelCache() *modelCache {
	return &modelCache{entries: make(map[string]modelCacheEntry)}
}

func (c *modelCache) Get(poolID string, now time.Time) ([]exposedModel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastSweep.IsZero() || now.Sub(c.lastSweep) >= modelCacheTTL {
		for cachedPoolID, cached := range c.entries {
			if !now.Before(cached.expiresAt) {
				delete(c.entries, cachedPoolID)
			}
		}
		c.lastSweep = now
	}
	entry, ok := c.entries[poolID]
	if !ok || !now.Before(entry.expiresAt) {
		delete(c.entries, poolID)
		return nil, false
	}
	return append([]exposedModel(nil), entry.models...), true
}

func (c *modelCache) Set(poolID string, models []exposedModel, expiresAt time.Time) {
	c.mu.Lock()
	c.entries[poolID] = modelCacheEntry{models: append([]exposedModel(nil), models...), expiresAt: expiresAt}
	c.mu.Unlock()
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	route, ok := s.authorize(w, r, "models")
	if !ok {
		return
	}
	if models, found := s.models.Get(route.Pool.ID, s.now()); found {
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
		return
	}
	if s.catalog == nil {
		writeOpenAIError(w, http.StatusServiceUnavailable, "model discovery is unavailable", "provider_error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	models, err := s.discoverPoolModels(ctx, route.Pool.ID)
	if err != nil {
		writeOpenAIError(w, http.StatusBadGateway, "failed to discover pool models", "provider_error")
		return
	}
	s.models.Set(route.Pool.ID, models, s.now().Add(modelCacheTTL))
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": models})
}

func (s *Server) discoverPoolModels(ctx context.Context, poolID string) ([]exposedModel, error) {
	accounts, err := s.store.ListPoolProviderAccounts(ctx, poolID)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, errors.New("pool has no routable provider accounts")
	}
	type result struct {
		provider string
		models   []domain.ProviderModel
		err      error
	}
	results := make(chan result, len(accounts))
	semaphore := make(chan struct{}, modelDiscoveryWorker)
	var wait sync.WaitGroup
	for _, account := range accounts {
		account := account
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results <- result{err: ctx.Err()}
				return
			}
			defer func() { <-semaphore }()
			models, listErr := s.catalog.ListAccount(ctx, account)
			results <- result{provider: account.Provider, models: models, err: listErr}
		}()
	}
	wait.Wait()
	close(results)

	byID := make(map[string]exposedModel)
	succeeded := false
	for discovered := range results {
		if discovered.err != nil {
			continue
		}
		succeeded = true
		for _, model := range discovered.models {
			if existing, ok := byID[model.ID]; ok && existing.OwnedBy != discovered.provider {
				existing.OwnedBy = "subpool"
				byID[model.ID] = existing
				continue
			}
			byID[model.ID] = exposedModel{ID: model.ID, Object: "model", OwnedBy: discovered.provider}
		}
	}
	if !succeeded {
		return nil, errors.New("all model discovery requests failed")
	}
	models := make([]exposedModel, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}
