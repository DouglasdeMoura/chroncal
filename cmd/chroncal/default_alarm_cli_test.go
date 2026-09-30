package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// setupDefaultAlarmCLIEnv prepares an isolated DB and a config file that
// enables one global default alarm. It returns the DB path.
func setupDefaultAlarmCLIEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := testutil.DBPath(t)
	t.Setenv("CHRONCAL_DB", dbPath)
	configDir := filepath.Join(dir, "xdg-config", "chroncal")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	config := "[alarms]\ndefault = [\"-PT15M\"]\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "xdg-state"))
	return dbPath
}

func TestDefaultAlarmEndToEndCLI(t *testing.T) {
	dbPath := setupDefaultAlarmCLIEnv(t)

	// Seed one synced-style event without alarms, 10 minutes old. Its
	// -PT15M default trigger fired 25 minutes ago, inside the stale window.
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	start := time.Now().Add(-10 * time.Minute)
	if _, err := a.Events.Create(context.Background(), event.CreateParams{
		CalendarID: 1,
		Title:      "Synced meeting",
		StartTime:  start,
		EndTime:    start.Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	a.Close()

	stdout, _, err := runChroncalCommand(t, "alarm", "check", "-o", "json")
	if err != nil {
		t.Fatalf("alarm check: %v", err)
	}
	var fired []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &fired); err != nil {
		t.Fatalf("parse alarm check output %q: %v", stdout, err)
	}
	if len(fired) != 1 {
		t.Fatalf("alarm check fired %d alarms, want 1: %s", len(fired), stdout)
	}
	if fired[0]["default"] != true {
		t.Fatalf("alarm check item = %v, want default=true", fired[0])
	}
	if fired[0]["alarm_id"] != nil {
		t.Fatalf("default alarm must carry no alarm_id, got %v", fired[0]["alarm_id"])
	}
	if fired[0]["trigger"] != "-PT15M" {
		t.Fatalf("trigger = %v, want -PT15M", fired[0]["trigger"])
	}
	stateID := fired[0]["state_id"].(float64)

	stdout, _, err = runChroncalCommand(t, "alarm", "list", "-o", "json")
	if err != nil {
		t.Fatalf("alarm list: %v", err)
	}
	var pending []map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &pending); err != nil {
		t.Fatalf("parse alarm list output %q: %v", stdout, err)
	}
	if len(pending) != 1 {
		t.Fatalf("alarm list shows %d alarms, want 1: %s", len(pending), stdout)
	}
	wantID := "d" + strconv.FormatInt(int64(stateID), 10)
	if pending[0]["id"] != wantID {
		t.Fatalf("pending id = %v, want %q", pending[0]["id"], wantID)
	}
	if pending[0]["default"] != true || pending[0]["action"] != "DISPLAY" {
		t.Fatalf("pending item = %v", pending[0])
	}

	// Snooze, then dismiss through the d-prefixed state ID.
	if _, _, err := runChroncalCommand(t, "alarm", "snooze", wantID, "--for", "10m"); err != nil {
		t.Fatalf("alarm snooze: %v", err)
	}
	stdout, _, err = runChroncalCommand(t, "alarm", "list", "-o", "json")
	if err != nil {
		t.Fatalf("alarm list after snooze: %v", err)
	}
	if !strings.Contains(stdout, "snoozed_to\":") {
		t.Fatalf("snoozed alarm list output = %s, want a snoozed_to value", stdout)
	}
	if _, _, err := runChroncalCommand(t, "alarm", "dismiss", wantID); err != nil {
		t.Fatalf("alarm dismiss: %v", err)
	}
	stdout, _, err = runChroncalCommand(t, "alarm", "list", "-o", "json")
	if err != nil {
		t.Fatalf("alarm list after dismiss: %v", err)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("alarm list after dismiss = %s, want []", stdout)
	}
}

func TestCalendarUpdateDefaultAlarmFlags(t *testing.T) {
	dbPath := setupDefaultAlarmCLIEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--default-alarm", "-PT15M", "--default-alarm", "AUDIO:-PT5M"); err != nil {
		t.Fatalf("calendar update --default-alarm: %v", err)
	}
	cals := listCalendarsForTest(t, dbPath)
	if got := *cals["Work"].DefaultAlarms; got != "-PT15M,AUDIO:-PT5M" {
		t.Fatalf("default_alarms = %q, want %q", got, "-PT15M,AUDIO:-PT5M")
	}

	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--default-alarm", "none"); err != nil {
		t.Fatalf("calendar update --default-alarm none: %v", err)
	}
	cals = listCalendarsForTest(t, dbPath)
	if cals["Work"].DefaultAlarms == nil || *cals["Work"].DefaultAlarms != "" {
		t.Fatalf("default_alarms = %v, want empty string (off)", cals["Work"].DefaultAlarms)
	}

	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--clear-default-alarms"); err != nil {
		t.Fatalf("calendar update --clear-default-alarms: %v", err)
	}
	cals = listCalendarsForTest(t, dbPath)
	if cals["Work"].DefaultAlarms != nil {
		t.Fatalf("default_alarms = %v, want nil (inherit)", *cals["Work"].DefaultAlarms)
	}

	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--default-alarm", "not-a-duration"); err == nil {
		t.Fatal("invalid --default-alarm spec must fail")
	}
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--default-alarm", "none", "--default-alarm", "-PT5M"); err == nil {
		t.Fatal("'none' combined with a spec must fail")
	}
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--default-alarm", "-PT5M", "--clear-default-alarms"); err == nil {
		t.Fatal("--default-alarm and --clear-default-alarms must be mutually exclusive")
	}
}

func TestCalendarGetJSONShowsDefaultAlarms(t *testing.T) {
	setupDefaultAlarmCLIEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--default-alarm", "-PT15M"); err != nil {
		t.Fatalf("calendar update: %v", err)
	}
	stdout, _, err := runChroncalCommand(t, "calendar", "list", "-o", "json")
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	// "Work" is not the template default calendar, so find it by name.
	var cals []struct {
		Name          string  `json:"name"`
		DefaultAlarms *string `json:"default_alarms"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &cals); err != nil {
		t.Fatalf("parse calendar list output %q: %v", stdout, err)
	}
	found := false
	for _, c := range cals {
		if c.Name == "Work" {
			found = true
			if c.DefaultAlarms == nil || *c.DefaultAlarms != "-PT15M" {
				t.Fatalf("Work default_alarms = %v, want -PT15M", c.DefaultAlarms)
			}
		}
	}
	if !found {
		t.Fatalf("calendar list output %s has no Work calendar", stdout)
	}
}

// listCalendarsForTest opens the DB and maps calendar name to row.
func listCalendarsForTest(t *testing.T, dbPath string) map[string]calendarRowForTest {
	t.Helper()
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	cals, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	out := make(map[string]calendarRowForTest, len(cals))
	for _, c := range cals {
		out[c.Name] = calendarRowForTest{DefaultAlarms: c.DefaultAlarms}
	}
	return out
}

type calendarRowForTest struct {
	DefaultAlarms *string
}
