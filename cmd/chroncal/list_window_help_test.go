package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// defaultWindowDays derives the number of days parseDateRange spans when no
// --from/--to are supplied. The help text can then be asserted against the real
// default rather than a hard-coded literal.
func defaultWindowDays(t *testing.T) int {
	t.Helper()
	from, to, err := parseDateRange("", "")
	if err != nil {
		t.Fatalf("parseDateRange: %v", err)
	}
	// Round rather than truncate: a 30-day local window that spans a DST
	// transition is 719 or 721 hours, not an exact multiple of 24.
	return int(math.Round(to.Sub(from).Hours() / 24))
}

// TestListToFlagHelpMatchesDefaultWindow guards the event list window help.
// The end is ui.event_list_days after --from. It is not a fixed count from now.
// The built-in count must match parseDateRange (issue #139).
// Todo and journal lists use an open default window (issue #304).
func TestListToFlagHelpMatchesDefaultWindow(t *testing.T) {
	days := defaultWindowDays(t)
	wantUsage := fmt.Sprintf("ui.event_list_days after --from; %d days by default", days)
	wantLong := fmt.Sprintf("The default is %d days.", days)

	cases := map[string]*cobra.Command{
		"event": eventListCmd(),
	}

	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			flag := cmd.Flags().Lookup("to")
			if flag == nil {
				t.Fatalf("%s list has no --to flag", name)
			}
			if !strings.Contains(flag.Usage, wantUsage) {
				t.Errorf("%s list --to help = %q, want it to mention %q", name, flag.Usage, wantUsage)
			}
			if strings.Contains(flag.Usage, "from now") {
				t.Errorf("%s list --to help = %q, must not say the window is from now", name, flag.Usage)
			}
			if !strings.Contains(cmd.Long, wantLong) {
				t.Errorf("%s list long help = %q, want it to mention %q", name, cmd.Long, wantLong)
			}
			if !strings.Contains(cmd.Long, "ui.event_list_days") {
				t.Errorf("%s list long help = %q, want ui.event_list_days", name, cmd.Long)
			}
		})
	}
}
