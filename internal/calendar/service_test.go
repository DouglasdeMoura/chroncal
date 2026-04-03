package calendar

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	return NewService(db, q)
}

func newTestServiceWithDB(t *testing.T) (*Service, *storage.Queries, *sql.DB) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	return NewService(db, q), q, db
}

func TestCalendarService_ListDefault(t *testing.T) {
	svc := newTestService(t)
	cals, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(cals) != 1 {
		t.Fatalf("List returned %d calendars, want 1", len(cals))
	}
	if cals[0].Name != "Personal" {
		t.Errorf("default calendar name = %q, want %q", cals[0].Name, "Personal")
	}
}

func TestCalendarService_Create(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, err := svc.Create(ctx, "Work", "#0284C7", "Work calendar")
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if c.ID == 0 {
		t.Error("Create returned ID 0")
	}
	if c.Name != "Work" {
		t.Errorf("Name = %q, want %q", c.Name, "Work")
	}
	if c.Color != "#0284C7" {
		t.Errorf("Color = %q, want %q", c.Color, "#0284C7")
	}
	if c.Description != "Work calendar" {
		t.Errorf("Description = %q, want %q", c.Description, "Work calendar")
	}

	cals, _ := svc.List(ctx)
	if len(cals) != 2 {
		t.Errorf("List after Create returned %d, want 2", len(cals))
	}
}

func TestCalendarService_Get(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get(1) error: %v", err)
	}
	if c.Name != "Personal" {
		t.Errorf("Get(1).Name = %q, want %q", c.Name, "Personal")
	}

	_, err = svc.Get(ctx, 999)
	if err == nil {
		t.Error("Get(999) expected error, got nil")
	}
}

func TestCalendarService_Update(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, err := svc.Update(ctx, 1, "Personal Updated", "#FF0000", "Updated desc")
	if err != nil {
		t.Fatalf("Update error: %v", err)
	}
	if c.Name != "Personal Updated" {
		t.Errorf("Name = %q, want %q", c.Name, "Personal Updated")
	}
	if c.Color != "#FF0000" {
		t.Errorf("Color = %q, want %q", c.Color, "#FF0000")
	}
}

func TestCalendarService_UpdateLinkedColorMarksDirty(t *testing.T) {
	svc, q, _ := newTestServiceWithDB(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        1,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("https://example.com/cal/work"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	c, err := svc.Update(ctx, 1, "Personal", "#123456", "")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !c.ColorDirty {
		t.Fatal("expected linked calendar color change to mark color dirty")
	}
}

func TestCalendarService_UpdateColorFromSync(t *testing.T) {
	svc, _, db := newTestServiceWithDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "UPDATE calendars SET color_dirty = 1 WHERE id = 1"); err != nil {
		t.Fatalf("seed dirty color: %v", err)
	}

	if err := svc.UpdateColorFromSync(ctx, 1, "#abcdef", "#abcdef"); err != nil {
		t.Fatalf("UpdateColorFromSync: %v", err)
	}

	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if cal.Color != "#abcdef" {
		t.Fatalf("Color = %q, want #abcdef", cal.Color)
	}
	if cal.RemoteColor != "#abcdef" {
		t.Fatalf("RemoteColor = %q, want #abcdef", cal.RemoteColor)
	}
	if cal.ColorDirty {
		t.Fatal("expected color dirty to be cleared")
	}
}

func TestCalendarService_Delete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	c, _ := svc.Create(ctx, "Temp", "#000", "")
	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	_, err := svc.Get(ctx, c.ID)
	if err == nil {
		t.Error("Get after Delete expected error, got nil")
	}
}

func TestCalendarService_DeleteCascade(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	cal, _ := svc.Create(ctx, "Temp", "#000", "")

	// Insert an event into this calendar directly
	_, err := db.ExecContext(ctx,
		"INSERT INTO events (uid, calendar_id, title, start_time, end_time, status, transp, class) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"test-uid", cal.ID, "Test Event", "2026-04-01T10:00:00Z", "2026-04-01T11:00:00Z", "CONFIRMED", "OPAQUE", "PUBLIC")
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// Delete calendar should cascade
	if err := svc.Delete(ctx, cal.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE calendar_id = ?", cal.ID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 events after cascade delete, got %d", count)
	}
}

func TestCalendarService_DeleteLastCalendarBlocked(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// The seed migration creates one "Personal" calendar (ID 1).
	// Deleting it should fail because it's the last one.
	err := svc.Delete(ctx, 1)
	if err == nil {
		t.Fatal("expected error when deleting the last calendar, got nil")
	}
	if !errors.Is(err, ErrLastCalendar) {
		t.Errorf("unexpected error: %v", err)
	}

	// Calendar should still exist.
	cal, err := svc.Get(ctx, 1)
	if err != nil {
		t.Fatalf("Get after blocked delete: %v", err)
	}
	if cal.Name != "Personal" {
		t.Errorf("calendar name = %q, want %q", cal.Name, "Personal")
	}
}

func TestCalendarService_DeleteNonLastCalendarAllowed(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create a second calendar, then delete it.
	c, err := svc.Create(ctx, "Work", "#0284C7", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := svc.Delete(ctx, c.ID); err != nil {
		t.Fatalf("Delete non-last calendar: %v", err)
	}

	// Only the original should remain.
	cals, _ := svc.List(ctx)
	if len(cals) != 1 {
		t.Fatalf("expected 1 calendar after delete, got %d", len(cals))
	}
}
