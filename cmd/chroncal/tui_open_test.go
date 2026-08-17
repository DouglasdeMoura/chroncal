package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
)

func TestResolveTUIOpenEvent_RequiresEventRef(t *testing.T) {
	_, err := resolveTUIOpenEvent(context.Background(), nil, "", "", "2026-04-21")
	if err == nil {
		t.Fatal("expected error for --at without --event")
	}
	if !strings.Contains(err.Error(), "--event") {
		t.Fatalf("error = %q, want mention of --event", err.Error())
	}
}

func TestResolveTUIOpenEvent_RejectsAtWithRecurrenceID(t *testing.T) {
	_, err := resolveTUIOpenEvent(context.Background(), nil, "series-uid", "2026-04-21T09:00:00Z", "2026-04-28T09:00:00Z")
	if err == nil {
		t.Fatal("expected error for --at with --recurrence-id")
	}
	if !strings.Contains(err.Error(), "--at") {
		t.Fatalf("error = %q, want mention of --at", err.Error())
	}
}

func TestRootEventFlag_UnknownEvent(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, stderr, err := runChroncalCommand(t, "--event", "999")
	if err == nil {
		t.Fatal("expected not_found for unknown --event")
	}
	if !strings.Contains(err.Error(), "not found") && !strings.Contains(stderr, "not found") {
		t.Fatalf("err=%v stderr=%q, want not found", err, stderr)
	}
}

func TestRootAtWithoutEvent_InvalidInput(t *testing.T) {
	setupCalendarCLITestEnv(t)

	_, _, err := runChroncalCommand(t, "--at", "2026-04-21")
	if err == nil {
		t.Fatal("expected invalid_input for --at without --event")
	}
	if !strings.Contains(err.Error(), "--event") {
		t.Fatalf("error = %q, want mention of --event", err.Error())
	}
}

func TestResolveTUIOpenEvent_AppliesAtAndKeepsDuration(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("TZ", "UTC")

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	stdout, _, err := runChroncalCommand(t,
		"event", "add", "Weekly review",
		"--calendar", "Work",
		"--date", "2026-04-03",
		"--time", "14:00",
		"--duration", "1h",
		"--rrule", "FREQ=WEEKLY;BYDAY=FR",
		"--output", "json",
	)
	if err != nil {
		t.Fatalf("event add: %v", err)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("decode add json: %v\n%s", err, stdout)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	if _, err := resolveTUIOpenEvent(context.Background(), a, "missing-uid", "", ""); err == nil {
		t.Fatal("expected not_found for unknown UID")
	}

	got, err := resolveTUIOpenEvent(context.Background(), a, strconv.FormatInt(created.ID, 10), "", "2026-04-17T14:00:00Z")
	if err != nil {
		t.Fatalf("resolveTUIOpenEvent: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("ID = %d, want %d", got.ID, created.ID)
	}
	if !got.StartTime.Equal(time.Date(2026, 4, 17, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("StartTime = %s, want 2026-04-17 14:00 UTC", got.StartTime)
	}
	if !got.EndTime.Equal(time.Date(2026, 4, 17, 15, 0, 0, 0, time.UTC)) {
		t.Fatalf("EndTime = %s, want 2026-04-17 15:00 UTC", got.EndTime)
	}
}
