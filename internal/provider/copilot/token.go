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
	"unicode"
)

const maxHTTPErrorDetailLength = 512

type HTTPError struct {
	StatusCode int
	Operation  string
	Detail     string
	RetryAfter string
}

type authorizationError struct {
	operation string
	detail    string
}

func (e *HTTPError) Error() string {
	message := fmt.Sprintf("%s returned status %d", e.Operation, e.StatusCode)
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *HTTPError) safeErrorDetail() string {
	if detail := normalizeErrorDetail(e.Detail); detail != "" {
		return detail
	}
	return fmt.Sprintf("%s returned status %d.", e.Operation, e.StatusCode)
}

func (e *authorizationError) Error() string {
	return e.operation + ": " + e.detail
}

func (e *authorizationError) safeErrorDetail() string {
	return e.detail
}

func SafeErrorDetail(err error) string {
	var detailed interface{ safeErrorDetail() string }
	if !errors.As(err, &detailed) {
		return ""
	}
	return detailed.safeErrorDetail()
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
		return accessToken{}, &HTTPError{
			StatusCode: resp.StatusCode,
			Operation:  "Copilot token exchange",
			Detail:     safeHTTPErrorDetail(body, githubToken),
			RetryAfter: resp.Header.Get("Retry-After"),
		}
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

func safeHTTPErrorDetail(body []byte, secrets ...string) string {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, field := range []string{"error_description", "message", "error"} {
		var detail string
		if err := json.Unmarshal(payload[field], &detail); err != nil {
			continue
		}
		if detail = normalizeErrorDetail(detail, secrets...); detail != "" {
			return detail
		}
	}
	return ""
}

func normalizeErrorDetail(detail string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			detail = strings.ReplaceAll(detail, secret, "[REDACTED]")
		}
	}
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, detail)
	detail = strings.Join(strings.Fields(detail), " ")
	runes := []rune(detail)
	if len(runes) > maxHTTPErrorDetailLength {
		detail = string(runes[:maxHTTPErrorDetailLength-3]) + "..."
	}
	return detail
}
