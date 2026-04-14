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
	ShowSidebar     bool    `json:"show_sidebar"`
	ViewMode        string  `json:"view_mode,omitempty"`
	HiddenCalendars []int64 `json:"hidden_calendars,omitempty"`
}

func defaultUIState() UIState {
	return UIState{ShowSidebar: true}
}

// LoadUIState reads persisted UI state. Missing or malformed files
// yield defaults; callers don't need to distinguish first-run from error.
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

func stateFile() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "chroncal", "state.json"), nil
}

// stateDir resolves XDG_STATE_HOME, falling back to ~/.local/state on
// Linux and os.UserConfigDir() on platforms without a state convention.
func stateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return dir, nil
	}
	if runtime.GOOS == "linux" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state"), nil
	}
	return os.UserConfigDir()
}
