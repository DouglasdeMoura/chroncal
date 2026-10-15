package tui

import (
	"strings"
	"testing"
)

// TestRunRefusesInvalidKeys starts the TUI with a [keys] table that binds
// one key to two actions. config.Load accepts the table, so the CLI
// continues to work. The TUI must refuse to start with the clear keys error, before
// it touches the terminal.
func TestRunRefusesInvalidKeys(t *testing.T) {
	err := Run(nil, "", RunOptions{Keys: map[string][]string{"quit": {"z"}, "sync": {"z"}}})
	if err == nil || !strings.Contains(err.Error(), `key "z" is bound to both quit and sync`) {
		t.Fatalf("Run = %v, want the keys error", err)
	}
}

// TestRunRefusesKeysOfWrongType starts the TUI with a [keys] value that
// is not a table. The TUI must refuse to start with the type error.
func TestRunRefusesKeysOfWrongType(t *testing.T) {
	err := Run(nil, "", RunOptions{Keys: "x"})
	if err == nil || !strings.Contains(err.Error(), "keys: the value must be a table of actions") {
		t.Fatalf("Run = %v, want the keys type error", err)
	}
}
