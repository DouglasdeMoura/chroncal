package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// The CLI's read-only display paths print whatever they can and warn about the
// rest. Routing them through the fail-fast Hydrate meant one unreadable
// relation blanked every relation read after it, so `event show --output json`
// emitted "attendees": null for attendees that exist — and a script consuming
// that JSON to mirror attendees elsewhere would delete them all.
func TestPopulateEventFields_KeepsLaterRelationsAfterAFailure(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "chroncal.db")
	t.Setenv("CHRONCAL_DB", dbPath)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-config"))
	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() { a.Close() })

	ctx := context.Background()
	e, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: 1,
		Title:      "Meeting",
		StartTime:  time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create event: %v", err)
	}
	if err := a.Events.ReplaceAttendees(ctx, e.ID, []model.Attendee{{
		Email: "someone@example.com", Role: "REQ-PARTICIPANT", RSVPStatus: "NEEDS-ACTION",
	}}); err != nil {
		t.Fatalf("replace attendees: %v", err)
	}

	// Alarms are read before attendees, so a failure there is what used to
	// swallow everything downstream.
	if _, err := a.DB.ExecContext(ctx, "ALTER TABLE event_alarms RENAME TO event_alarms_hidden"); err != nil {
		t.Fatalf("hide event_alarms: %v", err)
	}

	fetched, err := a.Events.Get(ctx, e.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	populateEventFields(ctx, a.Events, &fetched)

	if len(fetched.Attendees) != 1 {
		t.Errorf("Attendees = %d, want 1: a display path must degrade the field it "+
			"could not read, not every field after it", len(fetched.Attendees))
	}
}
