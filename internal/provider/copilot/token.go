package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPError struct {
	StatusCode int
	Operation  string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s returned status %d", e.Operation, e.StatusCode)
}

func IsDefinitiveAuthError(err error) bool {
	var httpError *HTTPError
	return errors.As(err, &httpError) && (httpError.StatusCode == http.StatusUnauthorized || httpError.StatusCode == http.StatusForbidden)
}

func exchangeToken(ctx context.Context, httpClient *http.Client, endpoint, githubToken string, now time.Time) (accessToken, error) {
	if strings.TrimSpace(githubToken) == "" {
		return accessToken{}, errors.New("GitHub token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return accessToken{}, fmt.Errorf("create Copilot token request: %w", err)
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/json")
	setEditorHeaders(req.Header)
	resp, err := httpClient.Do(req)
	if err != nil {
		return accessToken{}, fmt.Errorf("exchange Copilot token: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return accessToken{}, fmt.Errorf("read Copilot token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return accessToken{}, &HTTPError{StatusCode: resp.StatusCode, Operation: "Copilot token exchange"}
	}
	var payload struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		RefreshIn int64  `json:"refresh_in"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err = json.Unmarshal(body, &payload); err != nil {
		return accessToken{}, fmt.Errorf("decode Copilot token response: %w", err)
	}
	if strings.TrimSpace(payload.Token) == "" {
		return accessToken{}, errors.New("Copilot token response is missing token")
	}
	expiresAt := time.Unix(payload.ExpiresAt, 0)
	if payload.ExpiresAt <= 0 {
		expiresAt = now.Add(30 * time.Minute)
	}
	refreshAt := expiresAt.Add(-time.Minute)
	if payload.RefreshIn > 0 {
		refreshAt = now.Add(time.Duration(payload.RefreshIn) * time.Second).Add(-time.Minute)
	}
	latestRefresh := expiresAt.Add(-30 * time.Second)
	if refreshAt.After(latestRefresh) {
		refreshAt = latestRefresh
	}
	if refreshAt.Before(now) {
		refreshAt = now
	}
	return accessToken{
		value: payload.Token, apiBase: strings.TrimRight(strings.TrimSpace(payload.Endpoints.API), "/"),
		expiresAt: expiresAt, refreshAt: refreshAt,
	}, nil
}
