package domain

import (
	"math"
	"testing"
)

func TestRequestBodySettingsDefaultsAndValidation(t *testing.T) {
	settings := (Settings{}).WithRequestBodyDefaults(128, 512)
	if settings.MaxRequestBodyBytes != 128 || settings.MaxInflightRequestBodyBytes != 512 {
		t.Fatal("deployment defaults were not applied")
	}
	saved := Settings{MaxRequestBodyBytes: 256, MaxInflightRequestBodyBytes: 1024}
	if got := saved.WithRequestBodyDefaults(128, 512); got != saved {
		t.Fatal("deployment defaults replaced saved limits")
	}
	for _, test := range []struct{ max, budget int64 }{{0, 0}, {-1, 1024}, {256, 1023}, {math.MaxInt64, math.MaxInt64}} {
		if ValidateRequestBodyLimits(test.max, test.budget) == nil {
			t.Fatalf("invalid limits accepted: %#v", test)
		}
	}
	if err := ValidateRequestBodyLimits(256, 1024); err != nil {
		t.Fatal(err)
	}
}

func TestRequestBodySettingsPartialUpdates(t *testing.T) {
	defaults := Settings{MaxRequestBodyBytes: 128, MaxInflightRequestBodyBytes: 512}
	max, budget := int64(64), int64(256)
	for _, test := range []struct {
		name    string
		current Settings
		update  SettingsUpdate
		want    Settings
		invalid bool
	}{
		{"capacity preserves deployment defaults", Settings{}, SettingsUpdate{MaxAPIKeysPerAccount: 5}, Settings{MaxAPIKeysPerAccount: 5}, false},
		{"capacity preserves saved limits", defaults, SettingsUpdate{MaxAPIKeysPerAccount: 5}, Settings{MaxAPIKeysPerAccount: 5, MaxRequestBodyBytes: 128, MaxInflightRequestBodyBytes: 512}, false},
		{"body uses default budget", Settings{}, SettingsUpdate{MaxAPIKeysPerAccount: 5, MaxRequestBodyBytes: &max}, Settings{MaxAPIKeysPerAccount: 5, MaxRequestBodyBytes: 64, MaxInflightRequestBodyBytes: 512}, false},
		{"pair updates together", defaults, SettingsUpdate{MaxAPIKeysPerAccount: 5, MaxRequestBodyBytes: &max, MaxInflightRequestBodyBytes: &budget}, Settings{MaxAPIKeysPerAccount: 5, MaxRequestBodyBytes: 64, MaxInflightRequestBodyBytes: 256}, false},
		{"budget validates latest body", defaults, SettingsUpdate{MaxAPIKeysPerAccount: 5, MaxInflightRequestBodyBytes: &budget}, Settings{}, true},
		{"invalid capacity", defaults, SettingsUpdate{}, Settings{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.update.Apply(test.current, defaults)
			if (err != nil) != test.invalid || got != test.want {
				t.Fatalf("settings=%#v err=%v, want %#v invalid=%t", got, err, test.want, test.invalid)
			}
		})
	}
}
