package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestExportResource_ScopedToCalendar stores one series with the same UID
// in two calendars. The export of one calendar must hold the master and
// the override of that calendar only (issue #842). Before the fix, the
// push of calendar B could upload the series of calendar A.
func TestExportResource_ScopedToCalendar(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	for _, c := range []struct {
		id    int64
		title string
	}{{calA, "Standup A"}, {calB, "Standup B"}} {
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: "shared-series", CalendarID: c.id, Title: c.title,
			StartTime: start, EndTime: start.Add(30 * time.Minute),
			RecurrenceRule: "FREQ=DAILY;COUNT=5",
		}); err != nil {
			t.Fatalf("upsert master %q: %v", c.title, err)
		}
		if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
			UID: "shared-series", CalendarID: c.id, Title: c.title + " (late)",
			StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
			RecurrenceID: second.Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("upsert override %q: %v", c.title, err)
		}
	}

	for _, c := range []struct {
		id         int64
		want, deny string
	}{{calB, "Standup B", "Standup A"}, {calA, "Standup A", "Standup B"}} {
		data, err := engine.exportResource(ctx, c.id, "event", "shared-series")
		if err != nil {
			t.Fatalf("exportResource(calendar %d): %v", c.id, err)
		}
		body := string(data)
		if n := strings.Count(body, "BEGIN:VEVENT"); n != 2 {
			t.Errorf("export of calendar %d holds %d VEVENTs, want 2 (master and override):\n%s", c.id, n, body)
		}
		if !strings.Contains(body, "SUMMARY:"+c.want) {
			t.Errorf("export of calendar %d lacks %q:\n%s", c.id, c.want, body)
		}
		if strings.Contains(body, c.deny) {
			t.Errorf("export of calendar %d holds %q of the other calendar:\n%s", c.id, c.deny, body)
		}
	}

	// The conflict record and the organizer gate read the same master.
	idB, err := engine.lookupOwnerID(ctx, calB, "event", "shared-series")
	if err != nil {
		t.Fatalf("lookupOwnerID(B): %v", err)
	}
	masterB, err := engine.events.GetByCalendarUID(ctx, calB, "shared-series")
	if err != nil {
		t.Fatalf("GetByCalendarUID(B): %v", err)
	}
	if idB != masterB.ID {
		t.Errorf("lookupOwnerID(B) = %d, want the master of calendar B %d", idB, masterB.ID)
	}
}

// TestExportResource_LegacyOverrideCalendarHasNoResource: a calendar that
// holds legacy overrides of a UID only, while a live master of the UID is
// in another calendar, exports no resource. A PUT of the overrides alone
// would store an object with no master.
func TestExportResource_LegacyOverrideCalendarHasNoResource(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	res, err := engine.db.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	if err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	calC, _ := res.LastInsertId()

	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "legacy-series", CalendarID: calA, Title: "Standup A",
		StartTime: start, EndTime: start.Add(30 * time.Minute), RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}); err != nil {
		t.Fatalf("upsert master A: %v", err)
	}
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "legacy-series", CalendarID: calC, Title: "Legacy late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
		RecurrenceID: second.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}

	// One live master: the export of A carries the legacy override, and
	// the export of C has no resource.
	data, err := engine.exportResource(ctx, calA, "event", "legacy-series")
	if err != nil {
		t.Fatalf("exportResource(A): %v", err)
	}
	if !strings.Contains(string(data), "SUMMARY:Legacy late") {
		t.Errorf("export of A lacks the legacy override:\n%s", data)
	}
	if _, err := engine.exportResource(ctx, calC, "event", "legacy-series"); !errors.Is(err, errResourceMissing) {
		t.Errorf("exportResource(C) err = %v, want errResourceMissing", err)
	}

	// Two live masters: the export of C still has no resource.
	if _, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "legacy-series", CalendarID: calB, Title: "Standup B",
		StartTime: start, EndTime: start.Add(30 * time.Minute), RecurrenceRule: "FREQ=DAILY;COUNT=5",
	}); err != nil {
		t.Fatalf("upsert master B: %v", err)
	}
	if _, err := engine.exportResource(ctx, calC, "event", "legacy-series"); !errors.Is(err, errResourceMissing) {
		t.Errorf("exportResource(C) with two masters err = %v, want errResourceMissing", err)
	}
	// With two live masters the legacy row belongs to no series
	// (uidmove.LegacyOwner), so no export carries it.
	for _, cal := range []int64{calA, calB} {
		data, err := engine.exportResource(ctx, cal, "event", "legacy-series")
		if err != nil {
			t.Fatalf("exportResource(%d) with two masters: %v", cal, err)
		}
		if strings.Contains(string(data), "Legacy late") {
			t.Errorf("export of calendar %d with two masters carries the legacy override:\n%s", cal, data)
		}
	}
}

// TestExportResource_OrphanRowsBesideTrashedMaster: a calendar whose rows
// of a UID are live legacy overrides, while the one master of the UID is
// in the trash of another calendar, exports those rows as orphan
// instances. A row that a restore by ID brought back then reaches the
// server.
func TestExportResource_OrphanRowsBesideTrashedMaster(t *testing.T) {
	t.Parallel()
	engine, _, _, calA, calB := newICloudPairEngine(t)
	ctx := context.Background()

	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "orphan-series", CalendarID: calA, Title: "Standup A",
		StartTime: start, EndTime: start.Add(30 * time.Minute), RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("upsert master A: %v", err)
	}
	legacy, err := engine.events.UpsertByUID(ctx, event.UpsertParams{
		UID: "orphan-series", CalendarID: calB, Title: "Legacy late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour),
		RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	if err := engine.events.DeleteSeries(ctx, calA, master.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if err := engine.events.RestoreByID(ctx, legacy.ID); err != nil {
		t.Fatalf("RestoreByID(legacy): %v", err)
	}
	data, err := engine.exportResource(ctx, calB, "event", master.UID)
	if err != nil {
		t.Fatalf("exportResource(B): %v", err)
	}
	if !strings.Contains(string(data), "SUMMARY:Legacy late") {
		t.Errorf("export of B lacks the restored orphan row:\n%s", data)
	}
}
