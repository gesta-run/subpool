package domain

import (
	"fmt"
	"math"
)

const (
	DefaultMaxRequestBodyBytes         int64 = 256 << 20
	DefaultMaxInflightRequestBodyBytes int64 = 1 << 30
)

type SettingsUpdate struct {
	MaxAPIKeysPerAccount        int    `json:"max_api_keys_per_account"`
	MaxRequestBodyBytes         *int64 `json:"max_request_body_bytes"`
	MaxInflightRequestBodyBytes *int64 `json:"max_inflight_request_body_bytes"`
}

// Apply merges only supplied fields into the latest settings under a store lock.
func (update SettingsUpdate) Apply(settings, defaults Settings) (Settings, error) {
	if update.MaxAPIKeysPerAccount < 1 || update.MaxAPIKeysPerAccount > 100 {
		return Settings{}, fmt.Errorf("max_api_keys_per_account must be between 1 and 100")
	}
	settings.MaxAPIKeysPerAccount = update.MaxAPIKeysPerAccount
	if update.MaxRequestBodyBytes == nil && update.MaxInflightRequestBodyBytes == nil {
		return settings, nil
	}
	settings = settings.WithRequestBodyDefaults(defaults.MaxRequestBodyBytes, defaults.MaxInflightRequestBodyBytes)
	if update.MaxRequestBodyBytes != nil {
		settings.MaxRequestBodyBytes = *update.MaxRequestBodyBytes
	}
	if update.MaxInflightRequestBodyBytes != nil {
		settings.MaxInflightRequestBodyBytes = *update.MaxInflightRequestBodyBytes
	}
	if err := ValidateRequestBodyLimits(settings.MaxRequestBodyBytes, settings.MaxInflightRequestBodyBytes); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

// Zero values preserve the deployment defaults until an administrator saves limits.
func (settings Settings) WithRequestBodyDefaults(maxBytes, inflightBytes int64) Settings {
	if settings.MaxRequestBodyBytes == 0 && settings.MaxInflightRequestBodyBytes == 0 {
		settings.MaxRequestBodyBytes = maxBytes
		settings.MaxInflightRequestBodyBytes = inflightBytes
	}
	return settings
}

func ValidateRequestBodyLimits(maxBytes, inflightBytes int64) error {
	if maxBytes <= 0 || maxBytes > math.MaxInt64/4 {
		return fmt.Errorf("max_request_body_bytes must be positive and small enough for four buffered copies")
	}
	if inflightBytes < maxBytes*4 {
		return fmt.Errorf("max_inflight_request_body_bytes must be at least four times max_request_body_bytes")
	}
	return nil
}
