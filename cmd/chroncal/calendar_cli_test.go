package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/config"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

func TestCalendarCreateCreatesLocalOnlyCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	_, _, err := runChroncalCommand(t, "calendar", "create", "Work")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	cals, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	var found bool
	for _, got := range cals {
		if got.Name != "Work" {
			continue
		}
		found = true
		if got.AccountID != 0 {
			t.Fatalf("calendar account ID = %d, want 0 for local-only calendar", got.AccountID)
		}
		if got.RemoteURL != "" {
			t.Fatalf("calendar remote URL = %q, want empty for local-only calendar", got.RemoteURL)
		}
	}
	if !found {
		t.Fatalf("calendar list did not include %q", "Work")
	}
}

func TestCalendarCreateCanConnectRemoteCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")

	_, _, err := runChroncalCommand(t,
		"calendar", "create", "Work",
		"--remote-url", "https://cal.example.com/dav/calendars/work/",
		"--username", "alice",
		"--auth", "bearer",
		"--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("calendar create with remote config: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	cals, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	var found bool
	for _, got := range cals {
		if got.Name != "Work" {
			continue
		}
		found = true
		if got.AccountID == 0 {
			t.Fatalf("calendar account ID = 0, want connected calendar")
		}
		if got.RemoteURL != "https://cal.example.com/dav/calendars/work/" {
			t.Fatalf("calendar remote URL = %q, want %q", got.RemoteURL, "https://cal.example.com/dav/calendars/work/")
		}
	}
	if !found {
		t.Fatalf("calendar list did not include %q", "Work")
	}

	assertConnectedCalendarAndAccount(t, dbPath, "Work", "https://cal.example.com/dav/calendars/work/", "https://cal.example.com/dav", "bearer", "alice")
}

func TestCalendarUpdateCanConnectRemoteCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")

	_, _, err := runChroncalCommand(t, "calendar", "create", "Work")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	_, _, err = runChroncalCommand(t,
		"calendar", "update", "Work",
		"--remote-url", "https://cal.example.com/dav/calendars/work/",
		"--username", "alice",
		"--auth", "bearer",
		"--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("calendar update with remote config: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	assertConnectedCalendarAndAccount(t, dbPath, "Work", "https://cal.example.com/dav/calendars/work/", "https://cal.example.com/dav", "bearer", "alice")
}

// TestCalendarUpdatePreservesAuthTypeWhenReconnectingWithoutAuthFlag is the
// regression guard for issue #430. A re-point of --remote-url at an already
// linked calendar without --auth must keep the stored auth type
// (here "bearer"). It must not reset it to the "basic" flag default. That
// would prompt for a password and clobber the stored bearer/OAuth token.
func TestCalendarUpdatePreservesAuthTypeWhenReconnectingWithoutAuthFlag(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")

	createLinkedCalendarForTest(t, dbPath)

	_, _, err := runChroncalCommand(t,
		"calendar", "update", "Work",
		"--remote-url", "https://cal.example.com/dav/calendars/renamed/",
		"--username", "alice",
		"--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("calendar update with new remote URL: %v", err)
	}

	assertConnectedCalendarAndAccount(t, dbPath, "Work", "https://cal.example.com/dav/calendars/renamed/", "https://cal.example.com/dav", "bearer", "alice")
}

func TestCalendarUpdateCanDisconnectRemoteCalendar(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	_, accountID := createLinkedCalendarForTest(t, dbPath)

	_, _, err := runChroncalCommand(t, "calendar", "update", "Work", "--disconnect-remote")
	if err != nil {
		t.Fatalf("calendar update with disconnect flag: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	cals, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	var found bool
	for _, got := range cals {
		if got.Name != "Work" {
			continue
		}
		found = true
		if got.AccountID != 0 {
			t.Fatalf("calendar account ID = %d, want 0 after disconnect", got.AccountID)
		}
		if got.RemoteURL != "" {
			t.Fatalf("calendar remote URL = %q, want empty after disconnect", got.RemoteURL)
		}
	}
	if !found {
		t.Fatalf("calendar list did not include %q", "Work")
	}

	if _, err := a.Queries.GetAccount(context.Background(), accountID); err == nil {
		t.Fatalf("expected hidden account %d to be removed after disconnect", accountID)
	}
}

func TestCalendarUpdateRejectsDisconnectAndRemoteURLTogether(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	_, _, err := runChroncalCommand(t,
		"calendar", "update", "Work",
		"--disconnect-remote",
		"--remote-url", "https://cal.example.com/dav/calendars/work/",
	)
	if err == nil {
		t.Fatal("calendar update with disconnect and remote-url should fail")
	}
	if !strings.Contains(err.Error(), "disconnect") || !strings.Contains(err.Error(), "remote-url") {
		t.Fatalf("error = %v, want a clear validation failure mentioning both flags", err)
	}
}

func TestCalendarListJSONIncludesOwnerEmail(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	ctx := context.Background()
	cal, err := a.Calendars.Create(ctx, "Work", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if err := a.Calendars.SetOwnerEmail(ctx, cal.ID, "me@example.com"); err != nil {
		t.Fatalf("set owner email: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("close app: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "calendar", "list", "--output", "json")
	if err != nil {
		t.Fatalf("calendar list json: %v", err)
	}

	var cals []jsonCalendar
	if err := json.Unmarshal([]byte(stdout), &cals); err != nil {
		t.Fatalf("decode calendar json: %v\n%s", err, stdout)
	}
	for _, got := range cals {
		if got.Name == "Work" {
			if got.OwnerEmail != "me@example.com" {
				t.Fatalf("owner_email = %q, want %q", got.OwnerEmail, "me@example.com")
			}
			if got.AccountID != 0 {
				t.Fatalf("account_id = %d, want 0 for local calendar", got.AccountID)
			}
			if got.Hidden {
				t.Fatal("hidden = true, want false for a new calendar")
			}
			return
		}
	}
	t.Fatalf("calendar list did not include Work: %s", stdout)
}

func TestCalendarListJSONIncludesAccountForRemoteCalendar(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")

	_, _, err := runChroncalCommand(t,
		"calendar", "create", "Work",
		"--remote-url", "https://cal.example.com/dav/calendars/work/",
		"--username", "alice",
		"--auth", "bearer",
		"--allow-plaintext",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	stdout, _, err := runChroncalCommand(t, "calendar", "list", "--output", "json")
	if err != nil {
		t.Fatalf("calendar list json: %v", err)
	}
	var cals []jsonCalendar
	if err := json.Unmarshal([]byte(stdout), &cals); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout)
	}
	var got *jsonCalendar
	for i := range cals {
		if cals[i].Name == "Work" {
			got = &cals[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("Work missing: %s", stdout)
	}
	if got.AccountID == 0 {
		t.Fatalf("account_id omitted/zero, want linked account: %s", stdout)
	}
	if got.AccountName == "" {
		t.Fatalf("account_name empty, want derived display name: %s", stdout)
	}
	if got.RemoteURL != "https://cal.example.com/dav/calendars/work/" {
		t.Fatalf("remote_url = %q", got.RemoteURL)
	}
}

func TestCalendarHideShowJSON(t *testing.T) {
	setupCalendarCLITestEnv(t)
	if err := config.SaveUIState(config.UIState{
		ShowSidebar: false,
		ViewMode:    "week",
	}); err != nil {
		t.Fatalf("seed ui state: %v", err)
	}

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	work := calendarJSONByName(t, "Work")

	stdout, _, err := runChroncalCommand(t, "calendar", "hide", fmt.Sprintf("%d", work.ID), "--output", "json")
	if err != nil {
		t.Fatalf("calendar hide: %v", err)
	}
	var hidden jsonCalendar
	if err := json.Unmarshal([]byte(stdout), &hidden); err != nil {
		t.Fatalf("decode hide: %v\n%s", err, stdout)
	}
	if !hidden.Hidden || hidden.ID != work.ID {
		t.Fatalf("hide json = %+v, want hidden id %d", hidden, work.ID)
	}

	state := config.LoadUIState()
	if state.ShowSidebar || state.ViewMode != "week" {
		t.Fatalf("ui state scalars lost: %+v", state)
	}
	if len(state.HiddenCalendars) != 1 || state.HiddenCalendars[0] != work.ID {
		t.Fatalf("hidden_calendars = %v, want [%d]", state.HiddenCalendars, work.ID)
	}

	// Hide is idempotent and does not duplicate the id.
	if _, _, err := runChroncalCommand(t, "calendar", "hide", "Work", "--output", "json"); err != nil {
		t.Fatalf("calendar hide again: %v", err)
	}
	state = config.LoadUIState()
	if len(state.HiddenCalendars) != 1 || state.HiddenCalendars[0] != work.ID {
		t.Fatalf("hidden_calendars after second hide = %v", state.HiddenCalendars)
	}

	listed := calendarJSONByName(t, "Work")
	if !listed.Hidden {
		t.Fatal("list json hidden = false after hide")
	}

	stdout, _, err = runChroncalCommand(t, "calendar", "show", "Work", "--output", "json")
	if err != nil {
		t.Fatalf("calendar show: %v", err)
	}
	var shown jsonCalendar
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatalf("decode show: %v\n%s", err, stdout)
	}
	if shown.Hidden {
		t.Fatal("show json hidden = true, want false")
	}
	state = config.LoadUIState()
	if len(state.HiddenCalendars) != 0 {
		t.Fatalf("hidden_calendars after show = %v, want empty", state.HiddenCalendars)
	}
	if state.ViewMode != "week" || state.ShowSidebar {
		t.Fatalf("ui state scalars lost after show: %+v", state)
	}

	if _, _, err := runChroncalCommand(t, "calendar", "show", fmt.Sprintf("%d", work.ID), "--output", "json"); err != nil {
		t.Fatalf("calendar show again: %v", err)
	}
}

func TestCalendarHideUnknownIDIsNotFound(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t, "calendar", "hide", "99999", "--output", "json")
	if err == nil {
		t.Fatal("calendar hide 99999 should fail")
	}
	var payload struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
		t.Fatalf("decode error payload %q: %v", stderr, jerr)
	}
	// findCalendarByRef tags an unknown reference not_found itself, so
	// calendar hide/show, set-default, sync run/reset, and the --calendar
	// flag of every write command report the same code.
	if payload.Code != "not_found" {
		t.Fatalf("code = %q, want not_found", payload.Code)
	}
	if !strings.Contains(payload.Error, "99999") {
		t.Fatalf("error = %q, want it to mention the unknown id", payload.Error)
	}
}

// TestCalendarRefNotFoundCodeUniform guards the unified taxonomy: the same
// unknown calendar reference must report one code on every surface that
// resolves a reference. Before the fix, calendar hide/show re-tagged it
// invalid_input, sync re-tagged it not_found, and calendar set-default and
// the --calendar flag of write commands left it the generic error code.
func TestCalendarRefNotFoundCodeUniform(t *testing.T) {
	setupCalendarCLITestEnv(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	for _, args := range [][]string{
		{"calendar", "hide", "Ghost"},
		{"calendar", "set-default", "Ghost"},
		{"sync", "reset", "--calendar", "Ghost"},
		{"event", "add", "Title", "--calendar", "Ghost"},
	} {
		_, stderr, err := runChroncalCommand(t, append(args, "--output", "json")...)
		if err == nil {
			t.Fatalf("%s accepted an unknown calendar", strings.Join(args, " "))
		}
		var payload struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if jerr := json.Unmarshal([]byte(stderr), &payload); jerr != nil {
			t.Fatalf("%s: decode error payload %q: %v", strings.Join(args, " "), stderr, jerr)
		}
		if payload.Code != "not_found" {
			t.Fatalf("%s: code = %q, want not_found", strings.Join(args, " "), payload.Code)
		}
		if !strings.Contains(payload.Error, "Ghost") {
			t.Fatalf("%s: error = %q, want it to name the unknown reference", strings.Join(args, " "), payload.Error)
		}
	}
}

// TestCalendarDeleteAbortsWhenEventCountUnknown guards the destructive
// prompt. calendar delete swallowed the CountEventsByCalendar error, so
// the confirm quoted a zero count and the delete proceeded. The command
// must abort while the risk summary is unknown, and keep the calendar.
func TestCalendarDeleteAbortsWhenEventCountUnknown(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	// Break the events table so the count query fails, like an I/O error
	// would. Only the count reads it at this stage of the command.
	func() {
		a, err := app.New(dbPath)
		if err != nil {
			t.Fatalf("app.New: %v", err)
		}
		defer a.Close()
		if _, err := a.DB.ExecContext(context.Background(),
			"ALTER TABLE events RENAME TO events_hidden"); err != nil {
			t.Fatalf("hide events: %v", err)
		}
		t.Cleanup(func() {
			b, berr := app.New(dbPath)
			if berr != nil {
				t.Fatalf("app.New for restore: %v", berr)
			}
			defer b.Close()
			if _, err := b.DB.ExecContext(context.Background(),
				"ALTER TABLE events_hidden RENAME TO events"); err != nil {
				t.Fatalf("restore events: %v", err)
			}
		})
	}()

	var calID int64
	{
		a, err := app.New(dbPath)
		if err != nil {
			t.Fatalf("app.New: %v", err)
		}
		defer a.Close()
		cals, err := a.Calendars.List(context.Background())
		if err != nil {
			t.Fatalf("calendar list: %v", err)
		}
		for _, c := range cals {
			if c.Name == "Work" {
				calID = c.ID
			}
		}
		if calID == 0 {
			t.Fatalf("calendar Work missing before delete")
		}
	}

	_, stderr, err := runChroncalCommand(t, "calendar", "delete",
		strconv.FormatInt(calID, 10), "--yes", "--output", "json")
	if err == nil {
		t.Fatal("calendar delete must abort when the event count is unknown")
	}
	if !strings.Contains(stderr, "count events") {
		t.Fatalf("stderr = %q, want the count-events failure", stderr)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	if _, err := a.Calendars.Get(context.Background(), calID); err != nil {
		t.Fatalf("calendar %d was deleted despite the abort: %v", calID, err)
	}
}

func calendarJSONByName(t *testing.T, name string) jsonCalendar {
	t.Helper()
	stdout, _, err := runChroncalCommand(t, "calendar", "list", "--output", "json")
	if err != nil {
		t.Fatalf("calendar list: %v", err)
	}
	var cals []jsonCalendar
	if err := json.Unmarshal([]byte(stdout), &cals); err != nil {
		t.Fatalf("decode list: %v\n%s", err, stdout)
	}
	for _, c := range cals {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("calendar %q missing: %s", name, stdout)
	return jsonCalendar{}
}

func setupCalendarCLITestEnv(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chroncal.db")
	t.Setenv("CHRONCAL_DB", dbPath)
	// XDG_CONFIG_HOME points the plaintext credential store at this temp dir.
	// Connect flows pair this with --allow-plaintext so they store credentials
	// hermetically and don't depend on the host OS keyring, which is absent on
	// the Linux CI runner (no org.freedesktop.secrets) — see issue #474.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	// Isolate hidden_calendars from the developer's real TUI state.
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "xdg-state"))
	return dbPath
}

func runChroncalCommand(t *testing.T, args ...string) (string, string, error) {
	t.Helper()

	cmdArgs := append([]string{"-test.run=TestHelperProcess", "--"}, args...)
	cmd := exec.CommandContext(t.Context(), os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), stderr.String(), fmt.Errorf("%s", msg)
	}
	return stdout.String(), stderr.String(), nil
}

func assertConnectedCalendarAndAccount(t *testing.T, dbPath, calendarName, wantRemoteURL, wantServerURL, wantAuthType, wantUsername string) {
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

	var accountID int64
	for _, got := range cals {
		if got.Name != calendarName {
			continue
		}
		if got.AccountID == 0 {
			t.Fatalf("calendar account ID = 0, want connected calendar")
		}
		if got.RemoteURL != wantRemoteURL {
			t.Fatalf("calendar remote URL = %q, want %q", got.RemoteURL, wantRemoteURL)
		}
		accountID = got.AccountID
	}
	if accountID == 0 {
		t.Fatalf("calendar list did not include %q", calendarName)
	}

	acc, err := a.Queries.GetAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("GetAccount(%d): %v", accountID, err)
	}
	if acc.ServerUrl != wantServerURL {
		t.Fatalf("account server URL = %q, want %q", acc.ServerUrl, wantServerURL)
	}
	if acc.AuthType != wantAuthType {
		t.Fatalf("account auth type = %q, want %q", acc.AuthType, wantAuthType)
	}
	if acc.Username != wantUsername {
		t.Fatalf("account username = %q, want %q", acc.Username, wantUsername)
	}
}

func createLinkedCalendarForTest(t *testing.T, dbPath string) (int64, int64) {
	t.Helper()

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	ctx := context.Background()
	cal, err := a.Calendars.Create(ctx, "Work", "#7C3AED", "")
	if err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "__calendar_test",
		ServerUrl: "https://cal.example.com/dav",
		AuthType:  "bearer",
		Username:  "alice",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	if err := a.Calendars.LinkToAccount(ctx, cal.ID, account.ID, "https://cal.example.com/dav/calendars/work/"); err != nil {
		t.Fatalf("link calendar: %v", err)
	}

	return cal.ID, account.ID
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		fmt.Fprintln(os.Stderr, "helper process missing -- separator")
		os.Exit(2)
	}

	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	rootCmd.SetOut(os.Stdout)
	rootCmd.SetErr(os.Stderr)
	rootCmd.SetArgs(args[sep+1:])

	if err := rootCmd.Execute(); err != nil {
		// Mirror main()'s error path so tests see the same stderr format
		// real users do (text or structured JSON/YAML).
		printCLIError(err)
		os.Exit(1)
	}

	os.Exit(0)
}
