package main

import (
	"strings"
	"testing"
)

// TestTodoSearchCompletedAndIncompleteAreMutuallyExclusive reproduces issue
// #361. Both --completed and --incomplete on `todo search` were
// accepted in silence. The second flag was ignored rather than an error
// being returned. The user then saw misleading results.
func TestTodoSearchCompletedAndIncompleteAreMutuallyExclusive(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, _, err := runChroncalCommand(t,
		"todo", "search", "anything",
		"--completed", "--incomplete",
	)
	if err == nil {
		t.Fatal("todo search --completed --incomplete: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("todo search --completed --incomplete: error = %q, want it to mention %q",
			err.Error(), "mutually exclusive")
	}
}
