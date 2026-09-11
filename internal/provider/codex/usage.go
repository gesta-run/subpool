package codex

import "math"

type UsageWindow struct {
	UsedPercent      float64 `json:"used_percent"`
	RemainingPercent float64 `json:"remaining_percent"`
	WindowSeconds    int64   `json:"window_seconds"`
	ResetAt          int64   `json:"reset_at"`
}

type UsageSnapshot struct {
	PlanType     string       `json:"plan_type,omitempty"`
	UsageAllowed *bool        `json:"usage_allowed,omitempty"`
	LimitReason  string       `json:"limit_reason,omitempty"`
	FiveHour     *UsageWindow `json:"five_hour,omitempty"`
	Weekly       *UsageWindow `json:"weekly,omitempty"`
}

func (s *UsageSnapshot) markUsageBlocked(reason string) {
	allowed := false
	s.UsageAllowed = &allowed
	s.LimitReason = reason
}

func normalizeUsageWindow(usedPercent float64, windowSeconds, resetAt int64) *UsageWindow {
	used := math.Max(0, math.Min(100, usedPercent))
	return &UsageWindow{UsedPercent: used, RemainingPercent: 100 - used, WindowSeconds: windowSeconds, ResetAt: resetAt}
}
