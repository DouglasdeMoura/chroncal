package config

import (
	"fmt"
	"strings"
)

// Canonical ui.list_format values.
const (
	ListFormatDetail  = "detail"
	ListFormatCompact = "compact"
)

// ParseListFormat reports if s selects the compact text format.
// It accepts "detail" and "compact" in any case. An empty value selects
// the detail format. An unknown value returns an error.
func ParseListFormat(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", ListFormatDetail:
		return false, nil
	case ListFormatCompact:
		return true, nil
	default:
		return false, fmt.Errorf("invalid ui.list_format %q (must be %s or %s)", s, ListFormatDetail, ListFormatCompact)
	}
}

// ValidateEventListDays returns an error when days is not a positive number.
func ValidateEventListDays(days int) error {
	if days < 1 {
		return fmt.Errorf("invalid ui.event_list_days %d (must be greater than zero)", days)
	}
	return nil
}
