package config

import (
	"strings"
	"time"
)

// Canonical ui.week_start values.
const (
	WeekStartSunday = "sunday"
	WeekStartMonday = "monday"
)

// ParseWeekStart maps a config or UI-state value to a weekday.
// It accepts "sunday" and "monday" in any case, plus the short forms
// "sun" and "mon". An empty or unknown value returns Sunday and false.
func ParseWeekStart(s string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case WeekStartSunday, "sun", "su":
		return time.Sunday, true
	case WeekStartMonday, "mon", "mo":
		return time.Monday, true
	default:
		return time.Sunday, false
	}
}

// FormatWeekStart returns the canonical config value for w.
// Any weekday other than Monday maps to sunday.
func FormatWeekStart(w time.Weekday) string {
	if w == time.Monday {
		return WeekStartMonday
	}
	return WeekStartSunday
}
