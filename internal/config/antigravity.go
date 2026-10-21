package config

import (
	"errors"
	"net/url"
)

func (c Config) validateAntigravity() error {
	if (c.AntigravityClientID == "") != (c.AntigravityClientSecret == "") {
		return errors.New("configure both SUBPOOL_ANTIGRAVITY_CLIENT_ID and SUBPOOL_ANTIGRAVITY_CLIENT_SECRET")
	}
	base, err := url.Parse(c.AntigravityBaseURL)
	if err != nil || base.Scheme != "https" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("SUBPOOL_ANTIGRAVITY_BASE_URL must be an HTTPS URL without credentials, query or fragment")
	}
	return nil
}
