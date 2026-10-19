package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestICSDir creates a directory with one VEVENT .ics file in the
// style of a vdirsyncer collection and returns the directory path.
func writeTestICSDir(t *testing.T, uid, summary string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "collection")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + summary + "\r\n" +
		"DTSTART:20260501T090000Z\r\nDTEND:20260501T100000Z\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	if err := os.WriteFile(filepath.Join(dir, uid+".ics"), []byte(ics), 0o600); err != nil {
		t.Fatalf("write ics: %v", err)
	}
	return dir
}

// TestCalendarCreateWithPathStoresAndShowsIt covers the --path flag of
// calendar create and update, and the path row of calendar get (issue
// #809).
func TestCalendarCreateWithPathStoresAndShowsIt(t *testing.T) {
	setupCalendarCLITestEnv(t)
	dir := t.TempDir()

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Fastmail", "--path", dir+string(os.PathSeparator)); err != nil {
		t.Fatalf("calendar create --path: %v", err)
	}
	item := calendarJSONByName(t, "Fastmail")
	if item.LocalPath != filepath.Clean(dir) {
		t.Fatalf("local_path = %q, want %q (clean)", item.LocalPath, filepath.Clean(dir))
	}
	id := item.ID

	stdout, _, err := runChroncalCommand(t, "calendar", "get", jsonInt(id))
	if err != nil {
		t.Fatalf("calendar get: %v", err)
	}
	if !strings.Contains(stdout, "path") || !strings.Contains(stdout, filepath.Clean(dir)) {
		t.Fatalf("calendar get does not show the path:\n%s", stdout)
	}

	// An empty --path on update removes the directory.
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Fastmail", "--path", ""); err != nil {
		t.Fatalf("calendar update --path '': %v", err)
	}
	if got := calendarJSONByName(t, "Fastmail").LocalPath; got != "" {
		t.Fatalf("local_path after clear = %q, want empty", got)
	}
	stdout, _, err = runChroncalCommand(t, "calendar", "get", jsonInt(id))
	if err != nil {
		t.Fatalf("calendar get: %v", err)
	}
	if strings.Contains(stdout, "path") {
		t.Fatalf("calendar get still shows a path row:\n%s", stdout)
	}
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestConfigCalendarsAppliedOnLaunch verifies that the [[calendars]]
// entries of config.toml reach the database on the next command (issue
// #809). A tool such as home-manager can then declare the calendar list.
func TestConfigCalendarsAppliedOnLaunch(t *testing.T) {
	setupCalendarCLITestEnv(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chroncal")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dir := t.TempDir()
	content := "[[calendars]]\nname = \"Fastmail\"\npath = " + jsonString(dir) + "\n\n" +
		"[[calendars]]\nname = \"Work\"\ncolor = \"#0F766E\"\ndescription = \"Office\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	fm := calendarJSONByName(t, "Fastmail")
	if fm.LocalPath != dir {
		t.Fatalf("Fastmail local_path = %q, want %q", fm.LocalPath, dir)
	}
	work := calendarJSONByName(t, "Work")
	if work.Color != "#0F766E" || work.Description != "Office" {
		t.Fatalf("Work = %+v, want declared color and description", work)
	}

	// A changed entry updates the stored row. A removed entry leaves the
	// calendar in place.
	content = "[[calendars]]\nname = \"Fastmail\"\ncolor = \"#123456\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fm = calendarJSONByName(t, "Fastmail")
	if fm.Color != "#123456" || fm.LocalPath != dir {
		t.Fatalf("Fastmail after edit = %+v, want new color and the kept path", fm)
	}
	calendarJSONByName(t, "Work")
}

// TestConfigCalendarsInvalidEntryStopsCommand verifies a bad entry fails
// the command with a clear error, so a typo in home-manager settings does
// not run against a half-applied calendar list.
func TestConfigCalendarsInvalidEntryStopsCommand(t *testing.T) {
	setupCalendarCLITestEnv(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chroncal")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "[[calendars]]\npath = \"/tmp/no-name\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, _, err := runChroncalCommand(t, "calendar", "list")
	if err == nil || !strings.Contains(err.Error(), "calendars[0]: name is required") {
		t.Fatalf("calendar list error = %v, want the name-is-required error", err)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestIcalImportWithoutPathUsesCalendarDirectories covers the no-path mode
// of ical import (issue #809).
func TestIcalImportWithoutPathUsesCalendarDirectories(t *testing.T) {
	setupCalendarCLITestEnv(t)

	// No calendar has a directory yet.
	_, _, err := runChroncalCommand(t, "ical", "import")
	if err == nil || !strings.Contains(err.Error(), "no calendar has a directory") {
		t.Fatalf("ical import with no directories: error = %v", err)
	}

	fastmailDir := writeTestICSDir(t, "fm-1", "Fastmail event")
	workDir := writeTestICSDir(t, "work-1", "Work event")
	for _, args := range [][]string{
		{"calendar", "create", "Fastmail", "--path", fastmailDir},
		{"calendar", "create", "Work", "--path", workDir},
	} {
		if _, _, err := runChroncalCommand(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}

	// --calendar on a calendar without a directory is an error.
	_, _, err = runChroncalCommand(t, "ical", "import", "--calendar", "Personal")
	if err == nil || !strings.Contains(err.Error(), "has no directory") {
		t.Fatalf("ical import --calendar Personal: error = %v", err)
	}

	// One calendar from its directory.
	stdout, _, err := runChroncalCommand(t, "ical", "import", "--calendar", "Fastmail")
	if err != nil {
		t.Fatalf("ical import --calendar Fastmail: %v", err)
	}
	if !strings.Contains(stdout, "Imported 1 new") {
		t.Fatalf("single import output:\n%s", stdout)
	}

	// Every calendar with a directory. Fastmail is now an update, Work is
	// new. The batch output names each calendar.
	stdout, _, err = runChroncalCommand(t, "ical", "import")
	if err != nil {
		t.Fatalf("ical import: %v", err)
	}
	for _, want := range []string{
		"Fastmail (" + fastmailDir + "): Imported 0 new, updated 1 existing",
		"Work (" + workDir + "): Imported 1 new, updated 0 existing",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("batch output lacks %q:\n%s", want, stdout)
		}
	}

	// JSON mode lists one object per calendar.
	stdout, _, err = runChroncalCommand(t, "ical", "import", "--output", "json")
	if err != nil {
		t.Fatalf("ical import --output json: %v", err)
	}
	var runs []map[string]any
	if err := json.Unmarshal([]byte(stdout), &runs); err != nil {
		t.Fatalf("decode batch json: %v\n%s", err, stdout)
	}
	if len(runs) != 2 || runs[0]["calendar"] != "Fastmail" || runs[0]["path"] != fastmailDir {
		t.Fatalf("batch json = %v", runs)
	}

	// The events landed in their own calendars.
	fm := calendarJSONByName(t, "Fastmail")
	stdout, _, err = runChroncalCommand(t, "event", "list", "--calendar", jsonInt(fm.ID), "--from", "2026-05-01", "--to", "2026-05-02", "--output", "json")
	if err != nil {
		t.Fatalf("event list: %v", err)
	}
	if !strings.Contains(stdout, "Fastmail event") || strings.Contains(stdout, "Work event") {
		t.Fatalf("Fastmail events:\n%s", stdout)
	}

	// A missing directory in a batch run reports and continues.
	if err := os.RemoveAll(workDir); err != nil {
		t.Fatalf("remove work dir: %v", err)
	}
	stdout, stderr, err := runChroncalCommand(t, "ical", "import")
	if err == nil {
		t.Fatalf("ical import with a missing directory succeeded:\n%s", stdout)
	}
	if !strings.Contains(stderr, "Work") || !strings.Contains(stdout, "Fastmail (") {
		t.Fatalf("batch with a missing directory: stdout=%q stderr=%q", stdout, stderr)
	}
}

// TestIcalImportRejectsTwoPaths keeps the argument contract: at most one
// positional path.
func TestIcalImportRejectsTwoPaths(t *testing.T) {
	setupCalendarCLITestEnv(t)
	_, _, err := runChroncalCommand(t, "ical", "import", "a.ics", "b.ics")
	if err == nil || !strings.Contains(err.Error(), "at most 1 arg") {
		t.Fatalf("ical import with two paths: error = %v", err)
	}
}
