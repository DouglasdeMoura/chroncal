package sync

import (
	"context"
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
