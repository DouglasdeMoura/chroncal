package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
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
			return
		}
	}
	t.Fatalf("calendar list did not include Work: %s", stdout)
}

func setupCalendarCLITestEnv(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chroncal.db")
	t.Setenv("CHRONCAL_DB", dbPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
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
