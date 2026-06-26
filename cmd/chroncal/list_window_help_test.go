package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// defaultWindowDays derives the number of days parseDateRange spans when no
// --from/--to are supplied, so the help text can be asserted against the real
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

// TestListToFlagHelpMatchesDefaultWindow guards against the --to flag help text
// drifting from the actual default window in parseDateRange. The todo and
// journal list commands previously advertised "14 days" while the code
// defaulted to 30 (issue #139). Those retrospective lists now use an open
// default window (issue #304); only `event list` keeps the forward
// parseDateRange window this guard covers.
func TestListToFlagHelpMatchesDefaultWindow(t *testing.T) {
	want := fmt.Sprintf("%d days from now", defaultWindowDays(t))

	cases := map[string]*cobra.Command{
		"event": eventListCmd(),
	}

	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			flag := cmd.Flags().Lookup("to")
			if flag == nil {
				t.Fatalf("%s list has no --to flag", name)
			}
			if !strings.Contains(flag.Usage, want) {
				t.Errorf("%s list --to help = %q, want it to mention %q", name, flag.Usage, want)
			}
		})
	}
}
