package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempDir is t.TempDir with its symbolic links resolved. The stored path of
// a calendar resolves links too (fsutil.ExpandHome), and on macOS the temp
// root is a link, so a test compares resolved paths.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return dir
}

// writeTestICSDir creates a directory with one VEVENT .ics file in the
// style of a vdirsyncer collection and returns the directory path.
func writeTestICSDir(t *testing.T, uid, summary string) string {
	t.Helper()
	dir := filepath.Join(tempDir(t), "collection")
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
	dir := tempDir(t)

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
	dir := tempDir(t)
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

	// A missing directory in a batch run is a warning: the sync tool may
	// not have created it yet. The command still exits zero.
	if err := os.RemoveAll(workDir); err != nil {
		t.Fatalf("remove work dir: %v", err)
	}
	stdout, stderr, err := runChroncalCommand(t, "ical", "import")
	if err != nil {
		t.Fatalf("ical import with a missing directory must succeed: %v", err)
	}
	if !strings.Contains(stderr, "Work") || !strings.Contains(stderr, "does not exist yet") || !strings.Contains(stdout, "Fastmail (") {
		t.Fatalf("batch with a missing directory: stdout=%q stderr=%q", stdout, stderr)
	}

	// A directory that is a file is a real failure. The JSON array names
	// the failed calendar with its error, and the exit is non-zero.
	if err := os.WriteFile(workDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write file at work dir: %v", err)
	}
	stdout, _, err = runChroncalCommand(t, "ical", "import", "--output", "json")
	if err == nil {
		t.Fatalf("ical import --output json with a broken directory succeeded:\n%s", stdout)
	}
	runs = nil
	if err := json.Unmarshal([]byte(stdout), &runs); err != nil {
		t.Fatalf("decode batch json with a failure: %v\n%s", err, stdout)
	}
	if len(runs) != 2 || runs[1]["calendar"] != "Work" || runs[1]["error"] == nil || runs[1]["failed"] != float64(1) {
		t.Fatalf("batch json with a failure = %v", runs)
	}
}

// TestConfigCalendarsDeclaredRefusesDeleteAndRename verifies the CLI
// refuses to delete or rename a calendar that config.toml declares, since
// Apply would create it again on the next launch (issue #809).
func TestConfigCalendarsDeclaredRefusesDeleteAndRename(t *testing.T) {
	setupCalendarCLITestEnv(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chroncal")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "[[calendars]]\nname = \"Fastmail\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	fm := calendarJSONByName(t, "Fastmail")

	_, _, err := runChroncalCommand(t, "calendar", "delete", jsonInt(fm.ID), "--yes")
	if err == nil || !strings.Contains(err.Error(), "remove the entry first") {
		t.Fatalf("calendar delete of a declared calendar: error = %v", err)
	}
	_, _, err = runChroncalCommand(t, "calendar", "update", "Fastmail", "--name", "Mail")
	if err == nil || !strings.Contains(err.Error(), "remove the entry first") {
		t.Fatalf("calendar update --name of a declared calendar: error = %v", err)
	}
	// Other updates of a declared calendar still work.
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Fastmail", "--description", "Synced"); err != nil {
		t.Fatalf("calendar update --description of a declared calendar: %v", err)
	}
	calendarJSONByName(t, "Fastmail")

	// After the entry goes away, the delete works.
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "calendar", "delete", jsonInt(fm.ID), "--yes"); err != nil {
		t.Fatalf("calendar delete after the entry is removed: %v", err)
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

// TestIcalImportBatchShapeAndEmptyDirectory covers two batch-mode
// contracts: the JSON output is an array even for one target, and an
// empty stored directory imports nothing and succeeds.
func TestIcalImportBatchShapeAndEmptyDirectory(t *testing.T) {
	setupCalendarCLITestEnv(t)
	emptyDir := tempDir(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Holidays", "--path", emptyDir); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "ical", "import")
	if err != nil {
		t.Fatalf("ical import with an empty directory: %v", err)
	}
	if !strings.Contains(stdout, "Holidays (") || !strings.Contains(stdout, "Imported 0 new, updated 0 existing") {
		t.Fatalf("empty directory output:\n%s", stdout)
	}

	stdout, _, err = runChroncalCommand(t, "ical", "import", "--output", "json")
	if err != nil {
		t.Fatalf("ical import --output json: %v", err)
	}
	var runs []map[string]any
	if err := json.Unmarshal([]byte(stdout), &runs); err != nil {
		t.Fatalf("batch json with one target must be an array: %v\n%s", err, stdout)
	}
	if len(runs) != 1 || runs[0]["calendar"] != "Holidays" {
		t.Fatalf("batch json = %v", runs)
	}

	// An explicit empty directory is still an error, as before.
	if _, _, err := runChroncalCommand(t, "ical", "import", emptyDir, "--calendar", "Holidays"); err == nil {
		t.Fatal("ical import of an explicit empty directory succeeded")
	}
}

// TestIcalImportSinglePathJSONKeepsFreeBusyList guards the pre-existing
// JSON contract of a single import: "freebusy" is the parsed list, not a
// count, and the result is one object.
func TestIcalImportSinglePathJSONKeepsFreeBusyList(t *testing.T) {
	setupCalendarCLITestEnv(t)
	dir := writeTestICSDir(t, "single-1", "Single")
	stdout, _, err := runChroncalCommand(t, "ical", "import", dir, "--output", "json")
	if err != nil {
		t.Fatalf("ical import: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("single import json must be one object: %v\n%s", err, stdout)
	}
	// Master emitted the parsed list, which is null when the file holds no
	// VFREEBUSY. A count would be a number, so a number is the regression.
	switch v := out["freebusy"].(type) {
	case nil, []any:
	default:
		t.Fatalf("freebusy = %#v, want a list or null", v)
	}
}

// TestCalendarCreateRejectsBadColorAndPathWithRemote covers the --color
// validation that the config entries share, and the refusal of --path next
// to --remote-url.
func TestCalendarCreateRejectsBadColorAndPathWithRemote(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")

	_, _, err := runChroncalCommand(t, "calendar", "create", "Tinted", "--color", "teal")
	if err == nil || !strings.Contains(err.Error(), "hex color") {
		t.Fatalf("calendar create --color teal: error = %v", err)
	}
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Tinted", "--color", "#0f766e"); err != nil {
		t.Fatalf("calendar create --color #0f766e: %v", err)
	}
	if got := calendarJSONByName(t, "Tinted").Color; got != "#0F766E" {
		t.Fatalf("stored color = %q, want the normalized #0F766E", got)
	}

	_, _, err = runChroncalCommand(t, "calendar", "create", "Mixed",
		"--path", tempDir(t),
		"--remote-url", "https://cal.example.com/dav/calendars/work/", "--username", "alice", "--auth", "bearer")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("calendar create --path with --remote-url: error = %v", err)
	}

	// --path on a calendar that an account owns is refused by the service.
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Remote",
		"--remote-url", "https://cal.example.com/dav/calendars/remote/", "--username", "alice", "--auth", "bearer"); err != nil {
		t.Fatalf("calendar create remote: %v", err)
	}
	_, stderr, err := runChroncalCommand(t, "calendar", "update", "Remote", "--path", tempDir(t), "--output", "json")
	if err == nil || !strings.Contains(stderr, "\"code\": \"invalid_input\"") || !strings.Contains(stderr, "cannot have a local directory") {
		t.Fatalf("calendar update --path on a remote calendar: err=%v stderr=%q", err, stderr)
	}
	// A link of a calendar that has a directory is refused too.
	dir := tempDir(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Dir", "--path", dir); err != nil {
		t.Fatalf("calendar create --path: %v", err)
	}
	_, _, err = runChroncalCommand(t, "calendar", "update", "Dir",
		"--remote-url", "https://cal.example.com/dav/calendars/dir/", "--username", "alice", "--auth", "bearer")
	if err == nil || !strings.Contains(err.Error(), "has a local directory") {
		t.Fatalf("calendar update --remote-url on a calendar with a directory: error = %v", err)
	}
}

// TestIcalImportFollowsSymlinkedStoredDirectory verifies a stored directory
// that is a symbolic link imports in batch mode. A vdirsyncer layout often
// links the collection root to a mounted directory.
func TestIcalImportFollowsSymlinkedStoredDirectory(t *testing.T) {
	setupCalendarCLITestEnv(t)
	target := writeTestICSDir(t, "link-1", "Linked event")
	link := filepath.Join(tempDir(t), "calendars")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Linked", "--path", link); err != nil {
		t.Fatalf("calendar create --path: %v", err)
	}
	stdout, _, err := runChroncalCommand(t, "ical", "import", "--calendar", "Linked")
	if err != nil {
		t.Fatalf("ical import through a symlinked directory: %v", err)
	}
	if !strings.Contains(stdout, "Imported 1 new") {
		t.Fatalf("symlinked import output:\n%s", stdout)
	}
}

// TestConfigCalendarsDeclaredRefusesRemoteLink verifies a declared calendar
// cannot be linked to a CalDAV account, which would make every later launch
// fail in Apply.
func TestConfigCalendarsDeclaredRefusesRemoteLink(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chroncal")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[[calendars]]\nname = \"Fastmail\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	calendarJSONByName(t, "Fastmail")
	_, stderr, err := runChroncalCommand(t, "calendar", "update", "Fastmail",
		"--remote-url", "https://cal.example.com/dav/calendars/fm/", "--username", "alice", "--auth", "bearer", "--output", "json")
	if err == nil || !strings.Contains(stderr, "\"code\": \"invalid_input\"") || !strings.Contains(stderr, "remove the entry first") {
		t.Fatalf("link of a declared calendar: err=%v stderr=%q", err, stderr)
	}
	// Every later command still works.
	calendarJSONByName(t, "Fastmail")
}

// TestCalendarUpdateRejectsPathWithRemote verifies update refuses the flag
// pair before it writes, like create does.
func TestCalendarUpdateRejectsPathWithRemote(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	_, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--path", tempDir(t),
		"--remote-url", "https://cal.example.com/dav/calendars/work/", "--username", "alice", "--auth", "bearer")
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("calendar update --path with --remote-url: error = %v", err)
	}
	if got := calendarJSONByName(t, "Work").LocalPath; got != "" {
		t.Fatalf("the refused update wrote a path: %q", got)
	}
}

// TestConfigCalendarsConflictWarnsButRuns verifies a conflicting entry (a
// name that a CalDAV account owns) gives a warning and the command still
// runs, so the user can resolve the conflict with the CLI.
func TestConfigCalendarsConflictWarnsButRuns(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work",
		"--remote-url", "https://cal.example.com/dav/calendars/work/", "--username", "alice", "--auth", "bearer"); err != nil {
		t.Fatalf("calendar create remote: %v", err)
	}
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "chroncal")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[[calendars]]\nname = \"Work\"\ncolor = \"#123456\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	stdout, stderr, err := runChroncalCommand(t, "calendar", "list", "--output", "json")
	if err != nil {
		t.Fatalf("calendar list with a conflicting entry must run: %v", err)
	}
	if !strings.Contains(stderr, "[[calendars]] entry skipped") || !strings.Contains(stderr, "declares local calendars only") {
		t.Fatalf("missing skip warning on stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "\"name\": \"Work\"") || strings.Contains(stdout, "#123456") {
		t.Fatalf("the skipped entry changed the remote calendar:\n%s", stdout)
	}
	// The remote calendar is not bound, so --disconnect-remote resolves
	// the conflict from the CLI.
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--disconnect-remote"); err != nil {
		t.Fatalf("calendar update --disconnect-remote: %v", err)
	}
	if got := calendarJSONByName(t, "Work").Color; got != "#123456" {
		t.Fatalf("after the disconnect the entry must apply: color = %q", got)
	}
}

// TestCalendarPathFlagRejectsOtherUserHome verifies "~alice/dir" is an
// error, not a silent store of a directory below the working directory.
func TestCalendarPathFlagRejectsOtherUserHome(t *testing.T) {
	setupCalendarCLITestEnv(t)
	_, _, err := runChroncalCommand(t, "calendar", "create", "Odd", "--path", "~alice/calendars")
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("calendar create --path ~alice/...: error = %v", err)
	}
}

// TestCalendarUpdateRefusesLinkBeforeOtherWrites verifies a refused link
// leaves every other field of the calendar as it was.
func TestCalendarUpdateRefusesLinkBeforeOtherWrites(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Dir", "--path", tempDir(t), "--color", "#111111"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	_, _, err := runChroncalCommand(t, "calendar", "update", "Dir", "--color", "#222222",
		"--remote-url", "https://cal.example.com/dav/calendars/dir/", "--username", "alice", "--auth", "bearer")
	if err == nil || !strings.Contains(err.Error(), "has a local directory") {
		t.Fatalf("calendar update link of a directory calendar: error = %v", err)
	}
	if got := calendarJSONByName(t, "Dir").Color; got != "#111111" {
		t.Fatalf("the refused link changed the color: %q", got)
	}
}

// TestCalendarUpdateDisconnectThenPathInOneCommand verifies the path lands
// after the disconnect, so one command turns a server collection into a
// directory-fed calendar. The reverse, --path "" next to --remote-url, also
// works in one command.
func TestCalendarUpdateDisconnectThenPathInOneCommand(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work",
		"--remote-url", "https://cal.example.com/dav/calendars/work/", "--username", "alice", "--auth", "bearer"); err != nil {
		t.Fatalf("calendar create remote: %v", err)
	}
	dir := tempDir(t)
	// A path on a linked calendar without a disconnect is refused before
	// any write.
	_, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--name", "Deep", "--path", dir)
	if err == nil || !strings.Contains(err.Error(), "cannot have a local directory") {
		t.Fatalf("calendar update --path on a linked calendar: error = %v", err)
	}
	if calendarJSONByName(t, "Work").Name != "Work" {
		t.Fatal("the refused update renamed the calendar")
	}
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--disconnect-remote", "--path", dir); err != nil {
		t.Fatalf("calendar update --disconnect-remote --path: %v", err)
	}
	work := calendarJSONByName(t, "Work")
	if work.AccountID != 0 || work.LocalPath != dir {
		t.Fatalf("after disconnect+path = %+v, want local with the directory", work)
	}
	// --path "" clears the way for a link in the same command.
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--path", "",
		"--remote-url", "https://cal.example.com/dav/calendars/work/", "--username", "alice", "--auth", "bearer"); err != nil {
		t.Fatalf("calendar update --path \"\" --remote-url: %v", err)
	}
	work = calendarJSONByName(t, "Work")
	if work.AccountID == 0 || work.LocalPath != "" {
		t.Fatalf("after clear+link = %+v, want linked without a directory", work)
	}
}

// TestCalendarNameLookupPrefersExactMatch verifies the CLI resolves a name
// the way Apply does: an exact match wins over a case variant.
func TestCalendarNameLookupPrefersExactMatch(t *testing.T) {
	setupCalendarCLITestEnv(t)
	for _, name := range []string{"Work", "work"} {
		if _, _, err := runChroncalCommand(t, "calendar", "create", name); err != nil {
			t.Fatalf("calendar create %q: %v", name, err)
		}
	}
	if _, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--description", "exact"); err != nil {
		t.Fatalf("calendar update with an exact name: %v", err)
	}
	if got := calendarJSONByName(t, "Work").Description; got != "exact" {
		t.Fatalf("exact match not preferred: Work description = %q", got)
	}
	if got := calendarJSONByName(t, "work").Description; got != "" {
		t.Fatalf("the case variant changed: %q", got)
	}
	_, _, err := runChroncalCommand(t, "calendar", "update", "WORK", "--description", "x")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("a name with two case variants and no exact match must be ambiguous: %v", err)
	}
}

// TestCalendarPathOverlapCode covers the overlap rule on the CLI and its
// invalid_input code.
func TestCalendarPathOverlapCode(t *testing.T) {
	setupCalendarCLITestEnv(t)
	parent := tempDir(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "All", "--path", parent); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	_, stderr, err := runChroncalCommand(t, "calendar", "create", "Work", "--path", filepath.Join(parent, "work"), "--output", "json")
	if err == nil || !strings.Contains(stderr, "\"code\": \"invalid_input\"") || !strings.Contains(stderr, "its own tree") {
		t.Fatalf("calendar create inside another tree: err=%v stderr=%q", err, stderr)
	}
}

// TestIcalImportDanglingSymlinkIsMissingDirectory verifies a stored path
// that is a link to an absent target (an unmounted drive) gives the
// "does not exist yet" warning, not a symbolic-link refusal.
func TestIcalImportDanglingSymlinkIsMissingDirectory(t *testing.T) {
	setupCalendarCLITestEnv(t)
	link := filepath.Join(tempDir(t), "calendars")
	if err := os.Symlink(filepath.Join(tempDir(t), "absent"), link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Usb", "--path", link); err != nil {
		t.Fatalf("calendar create --path dangling link: %v", err)
	}
	_, stderr, err := runChroncalCommand(t, "ical", "import")
	if err != nil {
		t.Fatalf("ical import with a dangling link must succeed: %v", err)
	}
	if !strings.Contains(stderr, "does not exist yet") {
		t.Fatalf("missing the not-there-yet warning: %q", stderr)
	}
}

// TestCalendarUpdateRefusesOverlapBeforeOtherWrites verifies an overlap
// refusal leaves the other fields as they were.
func TestCalendarUpdateRefusesOverlapBeforeOtherWrites(t *testing.T) {
	setupCalendarCLITestEnv(t)
	parent := tempDir(t)
	for _, args := range [][]string{
		{"calendar", "create", "All", "--path", parent},
		{"calendar", "create", "Work", "--color", "#111111"},
	} {
		if _, _, err := runChroncalCommand(t, args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	_, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--color", "#222222", "--path", filepath.Join(parent, "work"))
	if err == nil || !strings.Contains(err.Error(), "its own tree") {
		t.Fatalf("calendar update with an overlapping path: error = %v", err)
	}
	if got := calendarJSONByName(t, "Work").Color; got != "#111111" {
		t.Fatalf("the refused update changed the color: %q", got)
	}
}
