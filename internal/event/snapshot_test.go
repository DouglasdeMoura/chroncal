package event

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
)

func TestDeleteWithSnapshot_Standalone_RoundTrip(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, CreateParams{
		CalendarID:  1,
		Title:       "Round-trip me",
		Description: "desc",
		Location:    "room 3",
		StartTime:   time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
		Categories:  "work,planning",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Populate transient children so the snapshot has real content to restore.
	if err := svc.ReplaceAlarms(ctx, created.ID, []model.Alarm{
		{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "heads up", Related: "START"},
	}); err != nil {
		t.Fatalf("seed alarms: %v", err)
	}
	if err := svc.ReplaceAttendees(ctx, created.ID, []model.Attendee{
		{Email: "a@example.com", Name: "A", Role: "REQ-PARTICIPANT", RSVPStatus: "NEEDS-ACTION"},
	}); err != nil {
		t.Fatalf("seed attendees: %v", err)
	}
	if err := svc.ReplaceComments(ctx, created.ID, []string{"first comment"}); err != nil {
		t.Fatalf("seed comments: %v", err)
	}

	snap, err := svc.DeleteWithSnapshot(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteWithSnapshot: %v", err)
	}
	if snap.Event.UID != created.UID {
		t.Fatalf("snapshot UID = %q, want %q", snap.Event.UID, created.UID)
	}
	if got := len(snap.Event.Alarms); got != 1 {
		t.Fatalf("snapshot alarms len = %d, want 1", got)
	}
	if got := len(snap.Event.Attendees); got != 1 {
		t.Fatalf("snapshot attendees len = %d, want 1", got)
	}
	if got := len(snap.Event.Comments); got != 1 {
		t.Fatalf("snapshot comments len = %d, want 1", got)
	}

	if _, err := svc.Get(ctx, created.ID); err == nil {
		t.Fatalf("expected Get to fail after delete")
	}

	newID, err := svc.Restore(ctx, snap)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if newID == 0 {
		t.Fatal("newID is 0")
	}
	if newID == created.ID {
		// New ID is allowed to collide if SQLite reuses the rowid; not a bug.
		t.Logf("new ID %d matches old ID (SQLite reuse)", newID)
	}

	restored, err := svc.Get(ctx, newID)
	if err != nil {
		t.Fatalf("Get after restore: %v", err)
	}
	if restored.UID != created.UID {
		t.Errorf("restored UID = %q, want %q", restored.UID, created.UID)
	}
	if restored.Title != "Round-trip me" {
		t.Errorf("restored Title = %q", restored.Title)
	}
	if restored.Description != "desc" {
		t.Errorf("restored Description = %q", restored.Description)
	}
	if restored.Location != "room 3" {
		t.Errorf("restored Location = %q", restored.Location)
	}

	alarms, err := svc.ListAlarms(ctx, newID)
	if err != nil {
		t.Fatalf("list alarms: %v", err)
	}
	if len(alarms) != 1 || alarms[0].TriggerValue != "-PT15M" {
		t.Errorf("alarms not restored: %+v", alarms)
	}

	attendees, err := svc.ListAttendees(ctx, newID)
	if err != nil {
		t.Fatalf("list attendees: %v", err)
	}
	if len(attendees) != 1 || attendees[0].Email != "a@example.com" {
		t.Errorf("attendees not restored: %+v", attendees)
	}

	comments, err := svc.ListComments(ctx, newID)
	if err != nil {
		t.Fatalf("list comments: %v", err)
	}
	if len(comments) != 1 || comments[0] != "first comment" {
		t.Errorf("comments not restored: %+v", comments)
	}
}

func TestDeleteWithSnapshot_MasterWithOverrides_ReturnsErrHasOverrides(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.Create(ctx, CreateParams{
		CalendarID:     1,
		Title:          "Weekly",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	override := CreateParams{
		CalendarID:   1,
		Title:        "Weekly (moved)",
		StartTime:    time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 10, 30, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T09:00:00Z",
	}
	// Create override with same UID as master.
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   override.CalendarID,
		Title:        override.Title,
		StartTime:    override.StartTime,
		EndTime:      override.EndTime,
		RecurrenceID: override.RecurrenceID,
	}); err != nil {
		t.Fatalf("create override: %v", err)
	}

	if _, err := svc.DeleteWithSnapshot(ctx, master.ID); !errors.Is(err, ErrHasOverrides) {
		t.Fatalf("DeleteWithSnapshot: got %v, want ErrHasOverrides", err)
	}
}

func TestRestore_RejectsConflictingSlot(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	created, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Original",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	snap, err := svc.DeleteWithSnapshot(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteWithSnapshot: %v", err)
	}

	// Re-create an event with the same UID before restore — simulates another
	// device (or a ghost import) claiming the slot. Restore should fail.
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:        created.UID,
		CalendarID: 1,
		Title:      "New occupant",
		StartTime:  time.Date(2026, 4, 2, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 2, 15, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("upsert conflicting slot: %v", err)
	}

	if _, err := svc.Restore(ctx, snap); err == nil {
		t.Fatal("expected Restore to fail when (uid, recurrence_id) is occupied")
	}
}

func TestEstimatedBytes_AccountsForAttachments(t *testing.T) {
	big := make([]byte, 1024*1024) // 1 MB
	snap := DeletedSnapshot{
		Event: Event{
			Title: "t",
			Attachments: []model.Attachment{
				{Data: big},
			},
		},
	}
	if n := snap.EstimatedBytes(); n < 1024*1024 {
		t.Fatalf("EstimatedBytes = %d, want >= 1 MiB", n)
	}
}
