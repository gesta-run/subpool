// Package people provides deployment-owned report display names and visibility.
package people

import (
	"encoding/json"
	"fmt"
	"strings"
)

type Directory struct {
	Names    map[string]string `json:"names"`
	Excluded []string          `json:"excluded"`
}

func Parse(raw string) (Directory, error) {
	d := Directory{}
	if strings.TrimSpace(raw) == "" {
		return d, nil
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return Directory{}, fmt.Errorf("invalid report people configuration")
	}
	names := map[string]string{}
	for alias, name := range d.Names {
		alias, name = normalize(alias), strings.TrimSpace(name)
		if alias == "" || name == "" {
			return Directory{}, fmt.Errorf("report display names must be nonempty")
		}
		if previous, exists := names[alias]; exists && previous != name {
			return Directory{}, fmt.Errorf("conflicting report display names")
		}
		names[alias] = name
	}
	d.Names = names
	for i, alias := range d.Excluded {
		d.Excluded[i] = normalize(alias)
	}
	return d, nil
}

// Name changes presentation only; source identities remain available for attribution.
func (d Directory) Name(current string, identities ...string) string {
	for _, alias := range identities {
		if name, ok := d.Names[normalize(alias)]; ok {
			return name
		}
	}
	if name, ok := d.Names[normalize(current)]; ok {
		return name
	}
	return current
}

func (d Directory) Hidden(identities ...string) bool {
	for _, identity := range identities {
		identity = normalize(identity)
		if identity == "" {
			continue
		}
		for _, excluded := range d.Excluded {
			if identity == excluded {
				return true
			}
		}
	}
	return false
}

func normalize(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
