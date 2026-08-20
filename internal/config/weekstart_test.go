package config

import (
	"testing"
	"time"
)

func TestParseWeekStart(t *testing.T) {
	tests := []struct {
		in     string
		want   time.Weekday
		wantOK bool
	}{
		{"", time.Sunday, false},
		{"   ", time.Sunday, false},
		{"bogus", time.Sunday, false},
		{"sunday", time.Sunday, true},
		{"Sunday", time.Sunday, true},
		{"SUN", time.Sunday, true},
		{" su ", time.Sunday, true},
		{"monday", time.Monday, true},
		{"Monday", time.Monday, true},
		{"MON", time.Monday, true},
		{"mo", time.Monday, true},
	}
	for _, tc := range tests {
		got, ok := ParseWeekStart(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("ParseWeekStart(%q) = %v, %v; want %v, %v",
				tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestFormatWeekStart(t *testing.T) {
	if got := FormatWeekStart(time.Monday); got != WeekStartMonday {
		t.Errorf("Monday = %q, want %q", got, WeekStartMonday)
	}
	if got := FormatWeekStart(time.Sunday); got != WeekStartSunday {
		t.Errorf("Sunday = %q, want %q", got, WeekStartSunday)
	}
	if got := FormatWeekStart(time.Saturday); got != WeekStartSunday {
		t.Errorf("Saturday = %q, want %q", got, WeekStartSunday)
	}
}
