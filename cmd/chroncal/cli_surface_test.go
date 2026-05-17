package main

import (
	"strings"
	"testing"
)

func TestRootCommandDoesNotRegisterAccount(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "account" {
			t.Fatal("root command should not register top-level account")
		}
	}
}

func TestCalendarCommandDoesNotRegisterLinkOrUnlink(t *testing.T) {
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() != "calendar" {
			continue
		}
		for _, sub := range cmd.Commands() {
			if sub.Name() == "link" || sub.Name() == "unlink" {
				t.Fatalf("calendar command should not register %q", sub.Name())
			}
		}
		return
	}

	t.Fatal("root command is missing calendar")
}

func TestSyncStatusEmptyMessageIsCalendarCentric(t *testing.T) {
	setupCalendarCLITestEnv(t)

	stdout, _, err := runChroncalCommand(t, "sync", "status")
	if err != nil {
		t.Fatalf("sync status: %v", err)
	}
	if !strings.Contains(stdout, "No connected calendars") {
		t.Fatalf("sync status output = %q, want calendar-centric empty-state message", stdout)
	}
	if strings.Contains(stdout, "account add") {
		t.Fatalf("sync status output = %q, should not mention account setup", stdout)
	}
}

func TestSyncStatusHonorsOutputJSON(t *testing.T) {
	setupCalendarCLITestEnv(t)

	stdout, _, err := runChroncalCommand(t, "sync", "status", "--output", "json")
	if err != nil {
		t.Fatalf("sync status --output json: %v", err)
	}
	got := strings.TrimSpace(stdout)
	if got != "[]" {
		t.Fatalf("sync status --output json = %q, want %q", got, "[]")
	}
}

func TestSyncConflictsHonorsOutputJSON(t *testing.T) {
	setupCalendarCLITestEnv(t)

	stdout, _, err := runChroncalCommand(t, "sync", "conflicts", "--output", "json")
	if err != nil {
		t.Fatalf("sync conflicts --output json: %v", err)
	}
	got := strings.TrimSpace(stdout)
	if got != "[]" {
		t.Fatalf("sync conflicts --output json = %q, want %q", got, "[]")
	}
}

func TestFreeBusyRemoteErrorUsesCalendarCentricLanguage(t *testing.T) {
	setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}

	_, _, err := runChroncalCommand(t,
		"freebusy",
		"--calendar", "Work",
		"--remote",
		"--from", "2026-04-01",
		"--to", "2026-04-07",
	)
	if err == nil {
		t.Fatal("remote freebusy on a local-only calendar should fail")
	}
	if !strings.Contains(err.Error(), "not connected to a remote calendar") {
		t.Fatalf("freebusy error = %q, want calendar-centric remote connection wording", err.Error())
	}
	if strings.Contains(err.Error(), "account") {
		t.Fatalf("freebusy error = %q, should not mention account", err.Error())
	}
}
