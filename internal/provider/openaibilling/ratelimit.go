package openaibilling

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

const RefreshInterval = 30 * time.Minute
const SnapshotFreshFor = RefreshInterval + 15*time.Minute

// RateLimitError contains only a known error code and a retry delay, never a
// credential, organization identifier, or upstream error message.
type RateLimitError struct {
	Code       string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("%s: Costs HTTP 429 (%s)", ErrUnavailable, e.Code)
}

func (e *RateLimitError) Unwrap() error { return ErrUnavailable }

func costsError(resp *http.Response, now time.Time) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("%w: Costs HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	code := "unknown"
	switch body.Error.Code {
	case "rate_limit_exceeded", "slow_down", "insufficient_quota", "organization_spend_limit_exceeded", "project_spend_limit_exceeded", "organization_usage_limit_exceeded":
		code = body.Error.Code
	}
	delay := time.Minute
	value := resp.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 && seconds <= int64((365*24*time.Hour)/time.Second) {
		delay = time.Duration(seconds) * time.Second
	} else if deadline, err := http.ParseTime(value); err == nil && deadline.After(now) {
		delay = deadline.Sub(now)
	}
	return &RateLimitError{Code: code, RetryAfter: delay}
}
