package control

import (
	"testing"
	"time"
)

func TestResetTimeDailyWindow(t *testing.T) {
	zone, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ expiry, want string }{
		{"2035-06-03T04:27:00+08:00", "2035-06-02T21:00:00+08:00"},
		{"2035-06-03T05:06:00+08:00", "2035-06-02T21:00:00+08:00"},
		{"2035-06-03T09:30:00+08:00", "2035-06-03T09:00:00+08:00"},
		{"2035-06-03T09:00:00+08:00", "2035-06-02T21:00:00+08:00"},
		{"2035-06-03T15:00:00+08:00", "2035-06-03T14:00:00+08:00"},
		{"2035-06-03T23:00:00+08:00", "2035-06-03T21:00:00+08:00"},
		{"2035-06-03T22:00:00+08:00", "2035-06-03T21:00:00+08:00"},
		{"2035-06-01T23:30:00Z", "2035-06-01T21:00:00+08:00"},
	} {
		t.Run(tt.expiry, func(t *testing.T) {
			expiry, err := time.Parse(time.RFC3339, tt.expiry)
			if err != nil {
				t.Fatal(err)
			}
			got := ResetTime(expiry, zone)
			if got.Format(time.RFC3339) != tt.want {
				t.Fatalf("got %s want %s", got.Format(time.RFC3339), tt.want)
			}
			if !got.Before(expiry) {
				t.Fatal("scheduled at or after expiry")
			}
		})
	}
}

func TestResetWindowDoesNotDependOnWeekday(t *testing.T) {
	zone, _ := time.LoadLocation("Asia/Shanghai")
	for day := 1; day <= 7; day++ {
		for _, tt := range []struct {
			hour, minute int
			want         bool
		}{{8, 59, false}, {9, 0, true}, {20, 59, true}, {21, 0, true}, {21, 1, false}, {23, 0, false}} {
			now := time.Date(2035, 6, day, tt.hour, tt.minute, 0, 0, zone)
			if resetWindowOpen(now, zone) != tt.want {
				t.Fatalf("incorrect window %s", now)
			}
		}
	}
}
