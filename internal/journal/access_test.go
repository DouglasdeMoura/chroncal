package journal

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
)

// setAccess mutates the seeded calendar 1's remote capability metadata so the
// access guard can be exercised against read-only and component-restricted
// collections.
func setAccess(t *testing.T, db *sql.DB, access, components string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		"UPDATE calendars SET remote_access = ?, remote_components = ? WHERE id = 1",
		access, components,
	); err != nil {
		t.Fatalf("set calendar access: %v", err)
	}
}

// requireAccessErr fails the test unless err wraps want via errors.Is.
func requireAccessErr(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestJournalCreate_ReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	setAccess(t, svc.db, "read", "VJOURNAL")

	_, err := svc.Create(ctx, CreateParams{CalendarID: 1, Summary: "X"})
	requireAccessErr(t, err, calendaraccess.ErrReadOnly)

	if all, _ := svc.List(ctx); len(all) != 0 {
		t.Fatalf("read-only Create persisted %d journal(s)", len(all))
	}
}

func TestJournalCreate_UnsupportedComponentRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	setAccess(t, svc.db, "owner", "VEVENT")

	_, err := svc.Create(ctx, CreateParams{CalendarID: 1, Summary: "X"})
	requireAccessErr(t, err, calendaraccess.ErrUnsupportedComponent)

	if all, _ := svc.List(ctx); len(all) != 0 {
		t.Fatalf("unsupported Create persisted %d journal(s)", len(all))
	}
}

func TestJournalUpdate_ReadOnlySourceRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc) // created on calendar 1 while still writable
	setAccess(t, svc.db, "read", "VJOURNAL")

	_, err := svc.Update(ctx, j.ID, UpdateParams{
		Summary:    "Changed",
		CalendarID: j.CalendarID,
	})
	requireAccessErr(t, err, calendaraccess.ErrReadOnly)

	got, gErr := svc.Get(ctx, j.ID)
	if gErr != nil {
		t.Fatalf("Get after rejected Update: %v", gErr)
	}
	if got.Summary != j.Summary {
		t.Fatalf("Summary = %q, want unchanged %q", got.Summary, j.Summary)
	}
}

// TestJournalUpdate_MoveToReadOnlyRejected covers the destination-calendar
// check: the source stays writable while the target calendar is read-only, so
// only the move leg is refused and the journal stays put.
func TestJournalUpdate_MoveToReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc) // calendar 1, writable

	if _, err := svc.db.ExecContext(ctx,
		"INSERT INTO calendars (name) VALUES ('Readonly Dest')"); err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE calendars SET remote_access = 'read', remote_components = 'VJOURNAL' WHERE id = 2"); err != nil {
		t.Fatalf("mark calendar 2 read-only: %v", err)
	}

	_, err := svc.Update(ctx, j.ID, UpdateParams{
		CalendarID: 2,
		Summary:    j.Summary,
	})
	requireAccessErr(t, err, calendaraccess.ErrReadOnly)

	got, gErr := svc.Get(ctx, j.ID)
	if gErr != nil {
		t.Fatalf("Get after rejected move: %v", gErr)
	}
	if got.CalendarID != j.CalendarID {
		t.Fatalf("CalendarID = %d, journal moved despite read-only destination", got.CalendarID)
	}
}

func TestJournalDelete_ReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)
	setAccess(t, svc.db, "read", "VJOURNAL")

	requireAccessErr(t, svc.Delete(ctx, j.ID), calendaraccess.ErrReadOnly)

	if _, err := svc.Get(ctx, j.ID); err != nil {
		t.Fatalf("journal soft-deleted despite read-only guard: %v", err)
	}
}

func TestJournalDelete_UnsupportedComponentRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)
	setAccess(t, svc.db, "owner", "VEVENT")

	requireAccessErr(t, svc.Delete(ctx, j.ID), calendaraccess.ErrUnsupportedComponent)

	if _, err := svc.Get(ctx, j.ID); err != nil {
		t.Fatalf("journal soft-deleted despite component guard: %v", err)
	}
}

func TestJournalDeleteSeries_ReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := createJournal(t, svc)
	setAccess(t, svc.db, "read", "VJOURNAL")

	requireAccessErr(t, svc.DeleteSeries(ctx, master.UID), calendaraccess.ErrReadOnly)

	if _, err := svc.GetByUID(ctx, master.UID); err != nil {
		t.Fatalf("series soft-deleted despite read-only guard: %v", err)
	}
}

func TestJournalRestoreByID_ReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)
	if err := svc.Delete(ctx, j.ID); err != nil { // soft-delete while writable
		t.Fatalf("setup delete: %v", err)
	}
	setAccess(t, svc.db, "read", "VJOURNAL")

	requireAccessErr(t, svc.RestoreByID(ctx, j.ID), calendaraccess.ErrReadOnly)

	// Still soft-deleted: the live lookup fails and the deleted marker remains.
	if _, err := svc.Get(ctx, j.ID); err == nil {
		t.Fatalf("journal restored despite read-only guard")
	}
	got, err := svc.GetIncludingDeleted(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetIncludingDeleted: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatalf("deleted_at cleared despite read-only guard")
	}
}

func TestJournalRestoreByUID_ReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	j := createJournal(t, svc)
	if err := svc.Delete(ctx, j.ID); err != nil { // soft-delete while writable
		t.Fatalf("setup delete: %v", err)
	}
	setAccess(t, svc.db, "read", "VJOURNAL")

	requireAccessErr(t, svc.RestoreByUID(ctx, j.UID), calendaraccess.ErrReadOnly)

	if _, err := svc.Get(ctx, j.ID); err == nil {
		t.Fatalf("journal restored despite read-only guard")
	}
	got, err := svc.GetIncludingDeleted(ctx, j.ID)
	if err != nil {
		t.Fatalf("GetIncludingDeleted: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatalf("deleted_at cleared despite read-only guard")
	}
}

// insertOrphanOverride inserts a journal row with a non-empty recurrence_id
// and the given UID but no matching master (recurrence_id = ”) row,
// reproducing an orphaned override/series-tail left behind by a purged master.
func insertOrphanOverride(t *testing.T, db *sql.DB, uid, recurrenceID string, softDeleted bool) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		"INSERT INTO journals (uid, calendar_id, summary, recurrence_id) VALUES (?, 1, 'Orphan', ?)",
		uid, recurrenceID,
	); err != nil {
		t.Fatalf("insert orphan override: %v", err)
	}
	if softDeleted {
		if _, err := db.ExecContext(context.Background(),
			"UPDATE journals SET deleted_at = '2026-01-01T00:00:00Z' WHERE uid = ? AND recurrence_id = ?",
			uid, recurrenceID,
		); err != nil {
			t.Fatalf("soft-delete orphan override: %v", err)
		}
	}
}

// TestJournalDeleteSeries_OrphanTailReadOnlyRejected reproduces the bypass:
// the master (recurrence_id = ”) is absent, so the master-only lookup would
// miss the orphaned override. The UID-wide guard must still resolve its
// calendar and refuse a read-only delete without mutating it.
func TestJournalDeleteSeries_OrphanTailReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const (
		uid          = "orphan-delete"
		recurrenceID = "2026-04-10T00:00:00Z"
	)
	insertOrphanOverride(t, svc.db, uid, recurrenceID, false)
	setAccess(t, svc.db, "read", "VJOURNAL")

	requireAccessErr(t, svc.DeleteSeries(ctx, uid), calendaraccess.ErrReadOnly)

	// The orphan override must remain live.
	if _, err := svc.GetByUIDAndRecurrenceID(ctx, uid, recurrenceID); err != nil {
		t.Fatalf("orphan override soft-deleted despite read-only guard: %v", err)
	}
}

// TestJournalRestoreByUID_OrphanTailReadOnlyRejected mirrors the delete case
// for the restore path: a soft-deleted override with no master must still be
// refused by the UID-wide guard and stay soft-deleted.
func TestJournalRestoreByUID_OrphanTailReadOnlyRejected(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const (
		uid          = "orphan-restore"
		recurrenceID = "2026-04-10T00:00:00Z"
	)
	insertOrphanOverride(t, svc.db, uid, recurrenceID, true)
	setAccess(t, svc.db, "read", "VJOURNAL")

	requireAccessErr(t, svc.RestoreByUID(ctx, uid), calendaraccess.ErrReadOnly)

	var deletedAt *string
	if err := svc.db.QueryRowContext(ctx,
		"SELECT deleted_at FROM journals WHERE uid = ? AND recurrence_id = ?",
		uid, recurrenceID,
	).Scan(&deletedAt); err != nil {
		t.Fatalf("query orphan override: %v", err)
	}
	if deletedAt == nil || *deletedAt == "" {
		t.Fatalf("orphan override restored (deleted_at cleared) despite read-only guard")
	}
}
