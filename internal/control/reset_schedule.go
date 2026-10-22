package control

import "time"

// ResetTime aims for one hour before expiry, within the daily 09:00–21:00 window.
// A morning expiry after 09:00 may use that day's 09:00 instead of the previous night.
func ResetTime(expiry time.Time, location *time.Location) time.Time {
	e := expiry.In(location)
	start := time.Date(e.Year(), e.Month(), e.Day(), 9, 0, 0, 0, location)
	end := time.Date(e.Year(), e.Month(), e.Day(), 21, 0, 0, 0, location)
	target := e.Add(-time.Hour)
	if !e.After(start) {
		return end.AddDate(0, 0, -1)
	}
	if target.Before(start) {
		return start
	}
	if target.After(end) {
		return end
	}
	return target
}

func resetWindowOpen(now time.Time, location *time.Location) bool {
	n := now.In(location)
	// Permit the 21:00 trigger minute, including normal timer/processing latency.
	return n.Hour() >= 9 && (n.Hour() < 21 || (n.Hour() == 21 && n.Minute() == 0))
}
