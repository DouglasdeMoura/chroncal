package main

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
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

// TestResolveTUIOpenEvent_AtFindsSeriesOccurrence guards issue #878. --at
// opens the occurrence of the series at its real time, and an override on
// that date gives its own row.
func TestResolveTUIOpenEvent_AtFindsSeriesOccurrence(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skipf("load zone: %v", err)
	}
	prev := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = prev })

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()

	// 09:00 in Sao Paulo is 12:00 UTC.
	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	master, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      start,
		EndTime:        start.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	inst := start.AddDate(0, 0, 1)
	moved := inst.Add(time.Hour)
	override, err := a.Events.UpdateInstance(ctx, master.CalendarID, master.UID, inst, event.UpdateParams{
		CalendarID: master.CalendarID, Title: "Moved standup", StartTime: moved, EndTime: moved.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	tests := []struct {
		name      string
		at        string
		wantID    int64
		wantTitle string
		wantStart time.Time
	}{
		{"date with override", "2026-10-19", override.ID, "Moved standup", moved},
		{"time of overridden slot", "2026-10-19T12:00:00Z", override.ID, "Moved standup", moved},
		{"date without override", "2026-10-20", master.ID, "Standup", start.AddDate(0, 0, 2)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTUIOpenEvent(ctx, a, master.UID, "", tc.at)
			if err != nil {
				t.Fatalf("resolveTUIOpenEvent: %v", err)
			}
			if got.ID != tc.wantID || got.Title != tc.wantTitle {
				t.Fatalf("got ID %d %q, want ID %d %q", got.ID, got.Title, tc.wantID, tc.wantTitle)
			}
			if !got.StartTime.Equal(tc.wantStart) {
				t.Fatalf("StartTime = %s, want %s", got.StartTime, tc.wantStart)
			}
			if got.EndTime.Sub(got.StartTime) != 30*time.Minute {
				t.Fatalf("span = %s, want 30m", got.EndTime.Sub(got.StartTime))
			}
		})
	}
}

// TestResolveTUIOpenEvent_AtEdgeCases covers the review cases of issue
// #878: an override moved far from its slot, a legacy override in another
// calendar, and an all-day series of two-day occurrences.
func TestResolveTUIOpenEvent_AtEdgeCases(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skipf("load zone: %v", err)
	}
	prev := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = prev })

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()
	other, err := a.Calendars.Create(ctx, "Other", "#112233", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}

	start := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	master, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Standup", StartTime: start, EndTime: start.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=DAILY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create series: %v", err)
	}
	slot := start.AddDate(0, 0, 1)
	far := slot.AddDate(0, 0, 4).Add(2 * time.Hour)
	moved, err := a.Events.UpdateInstance(ctx, 1, master.UID, slot, event.UpdateParams{
		CalendarID: 1, Title: "Moved far", StartTime: far, EndTime: far.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create moved override: %v", err)
	}
	legacySlot := start.AddDate(0, 0, 2)
	legacy, err := a.Events.UpdateInstance(ctx, 1, master.UID, legacySlot, event.UpdateParams{
		CalendarID: 1, Title: "Legacy", StartTime: legacySlot, EndTime: legacySlot.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("create legacy override: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE events SET calendar_id = ? WHERE id = ?`, other.ID, legacy.ID); err != nil {
		t.Fatalf("seed legacy override: %v", err)
	}

	allDay := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	trip, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Trip", StartTime: allDay, EndTime: allDay.AddDate(0, 0, 2), AllDay: true,
		RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("create all-day series: %v", err)
	}

	// 22:00 in Sao Paulo is 01:00 UTC on the next day.
	nightStart := time.Date(2026, 10, 19, 1, 0, 0, 0, time.UTC)
	night, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: 1, Title: "Night shift", StartTime: nightStart, EndTime: nightStart.Add(8 * time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create overnight series: %v", err)
	}
	if _, err := resolveTUIOpenEvent(ctx, a, trip.UID, "", "2026-10-22"); err == nil || !strings.Contains(err.Error(), "no occurrence on 2026-10-22") {
		t.Fatalf("a date with no occurrence gave %v, want an error", err)
	}

	tests := []struct {
		name      string
		ref, at   string
		wantID    int64
		wantStart time.Time
	}{
		{"slot of a far override", master.UID, "2026-10-19T12:00:00Z", moved.ID, far},
		{"overnight occurrence that starts on the date", night.UID, "2026-10-20", night.ID, nightStart.AddDate(0, 0, 2)},
		{"date of a legacy override", master.UID, "2026-10-20", legacy.ID, legacySlot},
		{"second day of an all-day occurrence", trip.UID, "2026-10-26", trip.ID, allDay.AddDate(0, 0, 7)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTUIOpenEvent(ctx, a, tc.ref, "", tc.at)
			if err != nil {
				t.Fatalf("resolveTUIOpenEvent: %v", err)
			}
			if got.ID != tc.wantID || !got.StartTime.Equal(tc.wantStart) {
				t.Fatalf("got ID %d at %s, want ID %d at %s", got.ID, got.StartTime, tc.wantID, tc.wantStart)
			}
		})
	}
}
