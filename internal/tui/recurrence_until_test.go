package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestFormatRRuleUntil_KeepsEndDayAcrossTimezones guards against issue #146:
// the ends-date arrives from the date picker as local midnight, which is the
// *start* of the chosen end day. UNTIL is inclusive, so a local-midnight UNTIL
// excludes every same-day occurrence (which fire at the event's start time,
// later than midnight). For a positive UTC offset the value even rolls back to
// the previous calendar day in UTC. The emitted UNTIL must instead cover the
// whole chosen end day in the user's zone, for every offset.
func TestFormatRRuleUntil_KeepsEndDayAcrossTimezones(t *testing.T) {
	cases := []struct {
		name      string
		offsetSec int
	}{
		{"positive offset (UTC+2)", 2 * 3600},
		{"large positive offset (UTC+14)", 14 * 3600},
		{"negative offset (UTC-10)", -10 * 3600},
		{"utc", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			loc := time.FixedZone(tc.name, tc.offsetSec)
			endsDate := time.Date(2026, 4, 30, 0, 0, 0, 0, loc) // local midnight

			got := formatRRuleUntil(endsDate)

			parsed, ok := parseRRuleUntil(strings.TrimPrefix(got, "UNTIL="))
			require.True(t, ok, "parseRRuleUntil(%q) failed", got)

			// Viewed in the user's zone, UNTIL must land at the end of the
			// chosen day so any same-day occurrence is included.
			local := parsed.In(loc)
			require.Equal(t, 2026, local.Year())
			require.Equal(t, time.April, local.Month())
			require.Equal(t, 30, local.Day(),
				"UNTIL should map back to Apr 30 in the user's zone, got %s", local)
			require.Equal(t, 23, local.Hour(),
				"UNTIL should anchor to end-of-day, not start-of-day, got %s", local)
		})
	}
}
