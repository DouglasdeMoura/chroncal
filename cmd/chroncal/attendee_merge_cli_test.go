package main

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// findAttendee returns the attendee with the given email, or false.
func findAttendee(atts []model.Attendee, email string) (model.Attendee, bool) {
	for _, a := range atts {
		if a.Email == email {
			return a, true
		}
	}
	return model.Attendee{}, false
}

// organizerEmail returns the email of the single organizer row, or "" if none.
func organizerEmail(atts []model.Attendee) string {
	for _, a := range atts {
		if a.Organizer {
			return a.Email
		}
	}
	return ""
}

// TestEventUpdateOrganizerPreservesAttendees verifies that updating only the
// organizer does not wipe existing (non-organizer) attendee rows, and that
// updating only attendees does not drop the existing organizer (issue #461).
func TestEventUpdateOrganizerPreservesAttendees(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"event", "add", "Standup",
		"--calendar", "Work",
		"--date", "2026-04-21",
		"--attendee", "Alice <alice@example.com>",
		"--organizer", "Bob <bob@example.com>",
	); err != nil {
		t.Fatalf("event add: %v", err)
	}

	// Update only the organizer; attendees must be preserved.
	if _, _, err := runChroncalCommand(t,
		"event", "update", "1",
		"--organizer", "Carol <carol@example.com>",
	); err != nil {
		t.Fatalf("event update --organizer: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	ctx := context.Background()

	atts, err := a.Events.ListAttendees(ctx, 1)
	if err != nil {
		t.Fatalf("ListAttendees: %v", err)
	}
	if _, ok := findAttendee(atts, "alice@example.com"); !ok {
		t.Fatalf("attendee alice@example.com was deleted by --organizer-only update; attendees=%+v", atts)
	}
	if got := organizerEmail(atts); got != "carol@example.com" {
		t.Fatalf("organizer = %q, want carol@example.com", got)
	}

	// Update only the attendee; organizer must be preserved.
	if _, _, err := runChroncalCommand(t,
		"event", "update", "1",
		"--attendee", "Dave <dave@example.com>",
	); err != nil {
		t.Fatalf("event update --attendee: %v", err)
	}

	atts, err = a.Events.ListAttendees(ctx, 1)
	if err != nil {
		t.Fatalf("ListAttendees: %v", err)
	}
	if got := organizerEmail(atts); got != "carol@example.com" {
		t.Fatalf("organizer = %q dropped by --attendee-only update; want carol@example.com; attendees=%+v", got, atts)
	}
	if _, ok := findAttendee(atts, "dave@example.com"); !ok {
		t.Fatalf("attendee dave@example.com not set; attendees=%+v", atts)
	}
}

// TestTodoUpdateOrganizerPreservesAttendees mirrors the event check for todos.
func TestTodoUpdateOrganizerPreservesAttendees(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"todo", "add", "Review PR",
		"--calendar", "Work",
		"--attendee", "Alice <alice@example.com>",
		"--organizer", "Bob <bob@example.com>",
	); err != nil {
		t.Fatalf("todo add: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"todo", "update", "1",
		"--organizer", "Carol <carol@example.com>",
	); err != nil {
		t.Fatalf("todo update --organizer: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	atts, err := a.Todos.ListAttendees(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListAttendees: %v", err)
	}
	if _, ok := findAttendee(atts, "alice@example.com"); !ok {
		t.Fatalf("attendee alice@example.com was deleted by --organizer-only update; attendees=%+v", atts)
	}
	if got := organizerEmail(atts); got != "carol@example.com" {
		t.Fatalf("organizer = %q, want carol@example.com", got)
	}
}

// TestJournalUpdateAttendeePreservesOrganizer mirrors the check for journals.
func TestJournalUpdateAttendeePreservesOrganizer(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)

	if _, _, err := runChroncalCommand(t, "calendar", "create", "Work"); err != nil {
		t.Fatalf("calendar create: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"journal", "add", "Sprint notes",
		"--calendar", "Work",
		"--date", "2026-04-01",
		"--attendee", "Alice <alice@example.com>",
		"--organizer", "Bob <bob@example.com>",
	); err != nil {
		t.Fatalf("journal add: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"journal", "update", "1",
		"--attendee", "Dave <dave@example.com>",
	); err != nil {
		t.Fatalf("journal update --attendee: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()

	atts, err := a.Journals.ListAttendees(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListAttendees: %v", err)
	}
	if got := organizerEmail(atts); got != "bob@example.com" {
		t.Fatalf("organizer = %q dropped by --attendee-only update; want bob@example.com; attendees=%+v", got, atts)
	}
	if _, ok := findAttendee(atts, "dave@example.com"); !ok {
		t.Fatalf("attendee dave@example.com not set; attendees=%+v", atts)
	}
}
