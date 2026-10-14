package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/gesta-run/subpool/internal/domain"
	"github.com/gesta-run/subpool/internal/store"
)

type fakeStore struct {
	store.Store
	quotaCalls              int
	quotaPreferred          domain.ProviderAccount
	settingsMu              sync.RWMutex
	settings                domain.Settings
	settingsErr             error
	route                   domain.KeyRoute
	sessionAccount          domain.ProviderAccount
	sessionErr              error
	reassigned              domain.ProviderAccount
	reassignedAccounts      []domain.ProviderAccount
	poolAccounts            []domain.ProviderAccount
	reassignExcludes        [][]string
	usageInput, usageOutput int64
	sessionSaved            bool
	savedAccount            string
	status                  []string
	cooldownUntil           *time.Time
	usageFailures           int
	sessionFailures         int
	usageCalls              int
	sessionCalls            int
	allowCalls              int
	usageCommitUncertain    bool
	usageEvents             map[string]struct{}
	healthFailureCodes      []string
	availabilityUpdates     []providerAvailabilityUpdate
	sessionSavedSignal      chan struct{}
}

func (f *fakeStore) GetSettings(context.Context) (domain.Settings, error) {
	f.settingsMu.RLock()
	defer f.settingsMu.RUnlock()
	return f.settings, f.settingsErr
}

func (f *fakeStore) setSettings(settings domain.Settings) {
	f.settingsMu.Lock()
	defer f.settingsMu.Unlock()
	f.settings = settings
}
