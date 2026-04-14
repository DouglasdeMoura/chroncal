package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUIStateRoundTrip_HiddenCalendars(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	in := UIState{
		ShowSidebar:     false,
		ViewMode:        "week",
		HiddenCalendars: []int64{2, 7, 13},
	}
	if err := SaveUIState(in); err != nil {
		t.Fatalf("SaveUIState: %v", err)
	}
	out := LoadUIState()
	if out.ShowSidebar != in.ShowSidebar || out.ViewMode != in.ViewMode {
		t.Errorf("scalar mismatch: got %+v want %+v", out, in)
	}
	if len(out.HiddenCalendars) != 3 || out.HiddenCalendars[0] != 2 ||
		out.HiddenCalendars[1] != 7 || out.HiddenCalendars[2] != 13 {
		t.Errorf("HiddenCalendars round-trip: got %v want %v", out.HiddenCalendars, in.HiddenCalendars)
	}
}

func TestUIStateLoad_OldFileWithoutHiddenCalendars(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	// Simulate a state file written by an older version (no hidden_calendars key).
	path := filepath.Join(dir, "chroncal", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload, _ := json.Marshal(map[string]any{"show_sidebar": true, "view_mode": "month"})
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	out := LoadUIState()
	if !out.ShowSidebar || out.ViewMode != "month" {
		t.Errorf("scalar fields lost: %+v", out)
	}
	if out.HiddenCalendars != nil {
		t.Errorf("HiddenCalendars should be nil for old file, got %v", out.HiddenCalendars)
	}
}
