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
	redirect, err := url.Parse(c.AntigravityRedirectURI)
	if err != nil || redirect.Host == "" || redirect.User != nil || redirect.RawQuery != "" || redirect.Fragment != "" {
		return errors.New("SUBPOOL_ANTIGRAVITY_REDIRECT_URI must be an HTTP(S) callback URL without credentials, query or fragment")
	}
	loopback := redirect.Hostname() == "localhost" || redirect.Hostname() == "127.0.0.1" || redirect.Hostname() == "::1"
	if redirect.Scheme != "https" && !(redirect.Scheme == "http" && loopback) {
		return errors.New("SUBPOOL_ANTIGRAVITY_REDIRECT_URI must use HTTPS, except for loopback callbacks")
	}
	return nil
}
