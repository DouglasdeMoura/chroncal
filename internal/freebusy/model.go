package freebusy

import (
	"strings"
	"time"
)

const (
	Busy            = "BUSY"
	BusyTentative   = "BUSY-TENTATIVE"
	BusyUnavailable = "BUSY-UNAVAILABLE"
)

// Period is a half-open busy interval [Start, End).
type Period struct {
	Start time.Time
	End   time.Time
	Type  string
}

// Result is a single VFREEBUSY window and its busy periods.
type Result struct {
	UID       string
	Organizer string
	URL       string
	DTStamp   time.Time
	Start     time.Time
	End       time.Time
	Periods   []Period
}

func normalizeType(kind string) string {
	switch strings.ToUpper(strings.TrimSpace(kind)) {
	case "", Busy:
		return Busy
	case BusyTentative:
		return BusyTentative
	case BusyUnavailable:
		return BusyUnavailable
	default:
		return Busy
	}
}

func typePriority(kind string) int {
	switch normalizeType(kind) {
	case BusyUnavailable:
		return 3
	case Busy:
		return 2
	case BusyTentative:
		return 1
	default:
		return 0
	}
}
