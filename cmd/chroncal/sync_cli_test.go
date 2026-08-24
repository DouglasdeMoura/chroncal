package main

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TestSyncResetMatchesCalendarCaseInsensitively guards against issue #112.
// `sync reset --calendar work` must match a calendar named "Work" the same
// way every other command's --calendar flag does (case-insensitive,
// strings.EqualFold). Before the fix the reset loop compared names with ==.
// A case-mismatched name then matched nothing in silence.
func TestSyncResetMatchesCalendarCaseInsensitively(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	stdout, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "reset", "--calendar", "work")
	if err != nil {
		t.Fatalf("sync reset --calendar work: %v (stderr: %s)", err, stderr)
	}
	if !strings.Contains(stdout, "Reset sync state") {
		t.Fatalf("sync reset --calendar work did not reset the %q calendar; stdout = %q", "Work", stdout)
	}
}

// TestSyncRunRejectsInvalidConflictStrategy guards against issue #215.
// `sync run --conflict <bad>` must be rejected up front. It must not
// fall back to server-wins in silence. That would discard local edits
// for a user who meant "prompt" but mistyped, e.g. "Prompt".
func TestSyncRunRejectsInvalidConflictStrategy(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	_, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "run", "--conflict", "Prompt")
	if err == nil {
		t.Fatalf("sync run --conflict Prompt was accepted; expected an invalid-input error")
	}
	if !strings.Contains(stderr, "conflict") || !strings.Contains(stderr, "Prompt") {
		t.Fatalf("sync run --conflict Prompt: error did not mention the bad value; stderr = %q", stderr)
	}
}

// TestSyncRunMatchesCalendarCaseInsensitively guards against issue #112:
// `sync run --calendar work` must resolve a calendar named "Work"
// case-insensitively. Before the fix the run loop compared names with ==,
// so it reported `calendar "work" not found`.
func TestSyncRunMatchesCalendarCaseInsensitively(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	// The run will still fail downstream (no stored credentials), but it must
	// not fail with the case-sensitive "not found" resolution error.
	_, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "run", "--calendar", "work")
	if err == nil {
		return // resolved and ran; resolution is clearly case-insensitive
	}
	if strings.Contains(stderr, `calendar "work" not found`) {
		t.Fatalf("sync run --calendar work failed to resolve %q case-insensitively; stderr = %q", "Work", stderr)
	}
}

// TestSyncRunRejectsAccountWithCalendar pins the flag contract of the
// account-scoped run. --calendar picks one local calendar while --account
// narrows the run to every calendar of one CalDAV account, so passing both
// is ambiguous. Reject the pair up front with invalid_input rather than
// silently honoring one of them.
func TestSyncRunRejectsAccountWithCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	_, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "run",
		"--account", "__calendar_test", "--calendar", "Work", "--output", "json")
	if err == nil {
		t.Fatalf("sync run --account with --calendar was accepted; expected an invalid_input error")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode stderr: %v\n%s", jerr, stderr)
	}
	if payload.Code != "invalid_input" || !strings.Contains(payload.Error, "mutually exclusive") {
		t.Fatalf("sync run --account + --calendar: code = %q, error = %q; want the mutually-exclusive invalid_input",
			payload.Code, payload.Error)
	}
}

// TestSyncRunScopesToRequestedAccount checks that --account narrows the run
// to that account's calendars only. The linked Work calendar belongs to the
// "__calendar_test" account, so a run scoped to a second, empty account must
// be a clean no-op (synced 0) rather than touch Work. A run scoped to the
// owning account — by case-mismatched name, matching every other account
// command — must resolve and report only Work, never a not_found error.
func TestSyncRunScopesToRequestedAccount(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	createLinkedCalendarForTest(t, dbPath)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	_, err = a.Queries.CreateAccount(context.Background(), storage.CreateAccountParams{
		Name:      "Empty",
		ServerUrl: "https://empty.example.com/dav",
		AuthType:  "bearer",
		Username:  "bob",
	})
	if err != nil {
		t.Fatalf("create empty account: %v", err)
	}
	a.Close()

	// An account with no linked calendars: the run is a no-op, not an error.
	stdout, _, err := runChroncalCommand(t, "sync", "--allow-plaintext", "run", "--account", "empty", "--output", "json")
	if err != nil {
		t.Fatalf("sync run --account empty: %v", err)
	}
	var emptyRun struct {
		Synced  int `json:"synced"`
		Results []struct {
			CalendarName string `json:"calendar_name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &emptyRun); err != nil {
		t.Fatalf("decode sync run --account empty output: %v\n%s", err, stdout)
	}
	if emptyRun.Synced != 0 || len(emptyRun.Results) != 0 {
		t.Fatalf("sync run --account empty synced %d calendar(s) (%v); want a no-op that leaves Work alone",
			emptyRun.Synced, emptyRun.Results)
	}

	// The owning account resolves case-insensitively. Without live CalDAV the
	// cycle may fail or never render; that is fine as long as the failure is
	// not account-not-found, and any rendered results stay inside the
	// account: Work is its only calendar, so anything else in results would
	// mean the run ignored --account.
	stdout, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "run", "--account", "__CALENDAR_TEST", "--output", "json")
	if err != nil && strings.Contains(stderr, "not_found") {
		t.Fatalf("sync run --account __CALENDAR_TEST failed to resolve the account case-insensitively; stderr = %q", stderr)
	}
	if !strings.Contains(stdout, "results") {
		return // failed before rendering; resolution already proved above
	}
	var scopedRun struct {
		Results []struct {
			CalendarName string `json:"calendar_name"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &scopedRun); err != nil {
		t.Fatalf("decode sync run --account __CALENDAR_TEST output: %v\n%s", err, stdout)
	}
	for _, r := range scopedRun.Results {
		if r.CalendarName != "Work" {
			t.Fatalf("sync run --account __CALENDAR_TEST synced %q; want only calendars of the requested account", r.CalendarName)
		}
	}
}

// TestSyncRunUnknownAccountIsNotFound keeps the account reference error in
// the same taxonomy as every other account command: an unknown name is a
// not_found cliError, so --output json consumers can dispatch on the code.
func TestSyncRunUnknownAccountIsNotFound(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t, "sync", "--allow-plaintext", "run", "--account", "ghost", "--output", "json")
	if err == nil {
		t.Fatalf("sync run --account ghost was accepted; expected a not_found error")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode stderr: %v\n%s", jerr, stderr)
	}
	if payload.Code != "not_found" || payload.Error != `account "ghost" not found` {
		t.Fatalf("sync run --account ghost: code = %q, error = %q; want the account not_found error",
			payload.Code, payload.Error)
	}
}

// TestSyncCommandsFailWhenCredentialStoreUnavailable guards the shared
// service construction in newSyncService. A dead session bus makes the
// keyring probe fail on every host. Each sync subcommand must then exit
// non-zero with the credential store error. A command must not run on
// with a nil store and fail later with a confusing error.
func TestSyncCommandsFailWhenCredentialStoreUnavailable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the session bus probe only applies on Linux")
	}
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_SECURITY_ALLOW_PLAINTEXT", "false")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/chroncal-test-bus")

	commands := [][]string{
		{"sync", "run"},
		{"sync", "status"},
		{"sync", "conflicts"},
		{"sync", "doctor"},
		{"sync", "resolve", "999999", "--pick", "local"},
		{"sync", "reset", "no-such-calendar"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			_, stderr, err := runChroncalCommand(t, args...)
			if err == nil {
				t.Fatalf("%v exited 0 with a broken credential store; want non-zero. stderr=%q", args, stderr)
			}
			if !strings.Contains(stderr, "credential store") {
				t.Fatalf("%v stderr = %q, want the credential store error", args, stderr)
			}
		})
	}
}
