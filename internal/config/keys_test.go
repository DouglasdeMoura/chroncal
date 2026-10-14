package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadWithConfigFile writes body to config.toml in a temporary
// XDG_CONFIG_HOME and calls Load.
func loadWithConfigFile(t *testing.T, body string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "chroncal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "chroncal", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
	return Load()
}

func TestLoad_KeysFromFile(t *testing.T) {
	cfg, err := loadWithConfigFile(t, `
[keys]
quit = ["z"]
month_view = ["M", "ctrl+m"]
`)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	keys := ResolveKeys(cfg.Keys)
	if got := strings.Join(keys["quit"], ","); got != "z" {
		t.Errorf("quit = %q, want z", got)
	}
	if got := strings.Join(keys["month_view"], ","); got != "M,ctrl+m" {
		t.Errorf("month_view = %q, want M,ctrl+m", got)
	}
	if got := strings.Join(keys["week_view"], ","); got != "w" {
		t.Errorf("week_view = %q, want the default w", got)
	}
}

func TestLoad_KeysErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{"unknown action", "[keys]\nfly = [\"f\"]\n", `unknown action "fly"`},
		{"empty list", "[keys]\nquit = []\n", "keys.quit: the key list is empty"},
		{"empty key", "[keys]\nquit = [\" \"]\n", "keys.quit: a key is empty"},
		{"duplicate between overrides", "[keys]\nquit = [\"z\"]\nsync = [\"z\"]\n", `key "z" is bound to both quit and sync`},
		{"duplicate with a default", "[keys]\nquit = [\"m\"]\n", `key "m" is bound to both quit and month_view`},
		{"duplicate shift alias", "[keys]\nsync = [\"shift+w\"]\n", "bound to both week_start and sync"},
		{"main view key", "[keys]\nsync = [\"j\"]\n", `keys.sync: chroncal reserves "j", because the month, week, day, and agenda views use it`},
		{"main view enter", "[keys]\ncreate = [\"enter\"]\n", `keys.create: chroncal reserves "enter"`},
		{"agenda key", "[keys]\nundo = [\"o\"]\n", `keys.undo: chroncal reserves "o"`},
		{"global key", "[keys]\nhelp = [\"esc\"]\n", `keys.help: chroncal reserves "esc"`},
		{"calendar manager key", "[keys]\nagenda_view = [\"A\"]\ncalendars = [\"a\"]\n", `keys.calendars: chroncal reserves "a", because the calendar manager uses it`},
		{"calendar manager shift alias", "[keys]\ncalendars = [\"shift+j\"]\n", `keys.calendars: chroncal reserves "shift+j"`},
		{"sidebar key", "[keys]\nsync = [\"K\"]\n", `keys.sync: chroncal reserves "K", because the sidebar uses it`},
		{"trash view key", "[keys]\nhelp = [\"r\"]\n", `keys.help: chroncal reserves "r", because the trash view uses it`},
		{"day dialog key","[keys]\ncreate = [\"n\"]\n", `keys.create: chroncal reserves "n", because the day dialog uses it`},
		{"event dialog key", "[keys]\nhelp = [\"p\"]\n", `keys.help: chroncal reserves "p"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadWithConfigFile(t, c.body)
			if err == nil {
				t.Fatalf("Load() error = nil, want %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load() error = %q, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestLoad_KeysSwapIsValid(t *testing.T) {
	// A swap moves two keys at once. The check uses the effective keys, so
	// the swap is valid.
	if _, err := loadWithConfigFile(t, "[keys]\nmonth_view = [\"w\"]\nweek_view = [\"m\"]\n"); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

// TestLoad_KeysReservedOnlyWhereTheActionRuns accepts a key that a dialog
// uses for an action that the dialog does not check. The day dialog uses
// n, but only the help and create keys reach the day dialog.
func TestLoad_KeysReservedOnlyWhereTheActionRuns(t *testing.T) {
	if _, err := loadWithConfigFile(t, "[keys]\nsync = [\"n\"]\nagenda_view = [\"A\"]\nday_view = [\"a\"]\n"); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestDefaultKeysHaveNoDuplicates(t *testing.T) {
	if err := ValidateKeys(nil); err != nil {
		t.Fatalf("ValidateKeys(nil) = %v", err)
	}
}

func TestResolveKeysTrimsAndRemovesRepeats(t *testing.T) {
	got := ResolveKeys(map[string][]string{"week_start": {" W ", "shift+w", "Z"}})["week_start"]
	if strings.Join(got, ",") != "W,Z" {
		t.Fatalf("week_start = %v, want [W Z]", got)
	}
}

// TestDefaultKeysAreNotReserved keeps the built-in keys valid: no reserved
// list holds a default key of an action that the list applies to.
func TestDefaultKeysAreNotReserved(t *testing.T) {
	for _, a := range KeyActions() {
		for _, k := range a.Defaults {
			if use := reservedKeyUse(a.Name, k); use != "" {
				t.Errorf("default key %q of %s is reserved, because %s", k, a.Name, use)
			}
		}
	}
}
