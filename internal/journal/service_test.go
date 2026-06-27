package journal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	return NewService(db, q)
}

func createJournal(t *testing.T, svc *Service) Journal {
	t.Helper()
	j, err := svc.Create(context.Background(), CreateParams{
		CalendarID: 1,
		Summary:    "Test Journal",
	})
	if err != nil {
		t.Fatalf("create journal: %v", err)
	}
	return j
}

func TestJournalService_Create(t *testing.T) {
	svc := newTestService(t)
	j := createJournal(t, svc)

	if j.ID == 0 {
		t.Error("ID is 0")
	}
	if j.UID == "" {
		t.Error("UID is empty")
	}
	if j.Summary != "Test Journal" {
		t.Errorf("Summary = %q, want %q", j.Summary, "Test Journal")
	}
	if j.Status != "FINAL" {
		t.Errorf("Status = %q, want %q", j.Status, "FINAL")
	}
	if j.Class != "PUBLIC" {
		t.Errorf("Class = %q, want %q", j.Class, "PUBLIC")
	}
}

func TestJournalService_Update(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createJournal(t, svc)

	updated, err := svc.Update(ctx, created.ID, UpdateParams{
		Summary:    "Updated Summary",
		Status:     "DRAFT",
		CalendarID: 1,
		Class:      "PRIVATE",
	})
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if updated.Summary != "Updated Summary" {
		t.Errorf("Summary = %q", updated.Summary)
	}
	if updated.Status != "DRAFT" {
		t.Errorf("Status = %q", updated.Status)
	}
	if updated.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", updated.Sequence)
	}
}

func TestJournalService_UpsertByUID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	p := UpsertParams{
		UID:        "test-upsert-uid",
		CalendarID: 1,
		Summary:    "Original",
	}

	first, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	p.Summary = "Updated"
	second, err := svc.UpsertByUID(ctx, p)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("upsert created new row: ID %d != %d", second.ID, first.ID)
	}
	if second.Summary != "Updated" {
		t.Errorf("Summary = %q, want %q", second.Summary, "Updated")
	}
}

func TestJournalService_Delete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createJournal(t, svc)

	if err := svc.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	_, err := svc.Get(ctx, created.ID)
	if err == nil {
		t.Error("Get after Delete expected error")
	}
}

func TestJournalDelete_MasterWithOverridesRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-master", CalendarID: 1, Summary: "Weekly Notes",
		StartDate:      time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RecurrenceRule: "FREQ=WEEKLY",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-master", CalendarID: 1, Summary: "Weekly Notes (moved)",
		StartDate:    time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: "2026-04-08T00:00:00Z",
	})

	master, _ := svc.GetByUID(ctx, "del-master")
	err := svc.Delete(ctx, master.ID)
	if !errors.Is(err, ErrHasOverrides) {
		t.Fatalf("Delete master with overrides: got %v, want ErrHasOverrides", err)
	}
}

// TestJournalDelete_RDateMasterWithOverridesRefused covers an RDATE-only
// recurring master (no RRULE). Its overrides must still block single-row
// deletion; otherwise the master is soft-deleted and the override rows are
// orphaned (issue #471, matching the #415 fix for events and todos).
func TestJournalDelete_RDateMasterWithOverridesRefused(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-rdate-master", CalendarID: 1, Summary: "RDATE series",
		StartDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RDates:    "2026-04-08T00:00:00Z",
	})
	svc.UpsertByUID(ctx, UpsertParams{
		UID: "del-rdate-master", CalendarID: 1, Summary: "RDATE series (moved)",
		StartDate:    time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC).Format(time.RFC3339),
		RecurrenceID: "2026-04-08T00:00:00Z",
	})

	master, _ := svc.GetByUID(ctx, "del-rdate-master")
	err := svc.Delete(ctx, master.ID)
	if !errors.Is(err, ErrHasOverrides) {
		t.Fatalf("Delete RDATE master with overrides: got %v, want ErrHasOverrides", err)
	}
}

func TestJournalService_GetByUID(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	created := createJournal(t, svc)

	got, err := svc.GetByUID(ctx, created.UID)
	if err != nil {
		t.Fatalf("GetByUID error: %v", err)
	}
	if got.Summary != created.Summary {
		t.Errorf("Summary = %q, want %q", got.Summary, created.Summary)
	}
	if got.ID != created.ID {
		t.Errorf("ID = %d, want %d", got.ID, created.ID)
	}
}

func TestJournalService_ReplaceCategories(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)

	err := svc.ReplaceCategories(ctx, j.ID, []string{"notes", "personal"})
	if err != nil {
		t.Fatalf("ReplaceCategories error: %v", err)
	}

	cats, err := svc.ListCategories(ctx, j.ID)
	if err != nil {
		t.Fatalf("ListCategories error: %v", err)
	}
	if len(cats) != 2 {
		t.Errorf("categories = %d, want 2", len(cats))
	}

	// Replace again with different categories
	err = svc.ReplaceCategories(ctx, j.ID, []string{"work"})
	if err != nil {
		t.Fatalf("second ReplaceCategories error: %v", err)
	}
	cats, _ = svc.ListCategories(ctx, j.ID)
	if len(cats) != 1 {
		t.Errorf("after re-replace: categories = %d, want 1", len(cats))
	}
}

func TestJournalService_ReplaceAttendees(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)

	err := svc.ReplaceAttendees(ctx, j.ID, []model.Attendee{
		{Email: "user@example.com", Name: "User", RSVPStatus: "ACCEPTED", Role: "REQ-PARTICIPANT"},
	})
	if err != nil {
		t.Fatalf("ReplaceAttendees error: %v", err)
	}

	attendees, err := svc.ListAttendees(ctx, j.ID)
	if err != nil {
		t.Fatalf("ListAttendees error: %v", err)
	}
	if len(attendees) != 1 {
		t.Errorf("attendees = %d, want 1", len(attendees))
	}
	if attendees[0].Email != "user@example.com" {
		t.Errorf("email = %q", attendees[0].Email)
	}
}

func TestJournalService_ReplaceComments(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)

	err := svc.ReplaceComments(ctx, j.ID, []string{"Comment one", "Comment two"})
	if err != nil {
		t.Fatalf("ReplaceComments error: %v", err)
	}

	comments, err := svc.ListComments(ctx, j.ID)
	if err != nil {
		t.Fatalf("ListComments error: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("comments = %d, want 2", len(comments))
	}
	if comments[0] != "Comment one" {
		t.Errorf("comment[0] = %q", comments[0])
	}
	if comments[1] != "Comment two" {
		t.Errorf("comment[1] = %q", comments[1])
	}
}
