package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// UIState is machine-written TUI state persisted across sessions.
// Unlike Config, users are not expected to hand-edit this file.
type UIState struct {
	ShowSidebar         bool    `json:"show_sidebar"`
	ViewMode            string  `json:"view_mode,omitempty"`
	HiddenCalendars     []int64 `json:"hidden_calendars,omitempty"`
	AgendaShowEmptyDays bool    `json:"agenda_show_empty_days,omitempty"`
	ShowWeekNumbers     bool    `json:"show_week_numbers,omitempty"`
	// WeekStart is "sunday" or "monday". Empty means no stored TUI choice
	// yet, so the ui.week_start config value (or Sunday) applies.
	WeekStart string `json:"week_start,omitempty"`
}

func defaultUIState() UIState {
	return UIState{ShowSidebar: true}
}

// LoadUIState reads persisted UI state. Files that are gone or malformed
// yield defaults. Callers do not need to distinguish first-run from error.
func LoadUIState() UIState {
	path, err := stateFile()
	if err != nil {
		return defaultUIState()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultUIState()
	}
	s := defaultUIState()
	if err := json.Unmarshal(data, &s); err != nil {
		return defaultUIState()
	}
	return s
}

// SaveUIState writes UI state atomically via a temp file + rename.
func SaveUIState(s UIState) error {
	path, err := stateFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LogFilePath returns the path of the background-job log file. Jobs that
// run while the TUI owns the terminal (the soft-delete purger) log here
// instead of stderr. Failures then stay inspectable. They do not print over
// the display.
func LogFilePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chroncal", "chroncal.log"), nil
}

func stateFile() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chroncal", "state.json"), nil
}

// stateDir resolves XDG_STATE_HOME. It falls back to ~/.local/state on
// Linux and os.UserConfigDir() on platforms without a state convention.
func stateDir() (string, error) {
	return BaseDir("XDG_STATE_HOME", runtime.GOOS, ".local", "state")
}
